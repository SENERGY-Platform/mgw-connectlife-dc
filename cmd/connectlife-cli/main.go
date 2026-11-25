/*
 * Copyright (c) 2026 InfAI (CC SES)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// connectlife-cli runs single functions of the connector against the real ConnectLife cloud.
// It reads the same environment variables as the connector. Only "set" writes to an appliance;
// nothing writes to the platform.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/config"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/devicetype"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/platform"
	"github.com/SENERGY-Platform/models/go/models"
)

const usage = `usage: connectlife-cli [-json] <command> [args]

commands:
  login                         log in and fetch the appliance list once
  devices                       list appliances
  status <device>               current status list
  properties <device>           properties of the model with current value, type, read/write and value list
  property-list <device>        property metadata of the model as the cloud returns it (raw JSON)
  devicetype <device>           device type the connector would create (JSON)
  plan [-platform-url URL]      per appliance model: existing device type or "would create"
  set <device> <property> <value>   write one property to the appliance

<device> is the deviceId, puid or nickname of an appliance.
`

func main() {
	asJson := flag.Bool("json", false, "print JSON instead of tables")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Args()[1:], *asJson); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(command string, args []string, asJson bool) error {
	cfg, err := config.New("")
	if err != nil {
		return err
	}
	client, err := connectlife.New(connectlife.Credentials{
		Username:          cfg.ConnectLifeUsername,
		Password:          cfg.ConnectLifePassword.Value(),
		GigyaApiKey:       cfg.ConnectLifeGigyaApiKey.Value(),
		OAuthClientSecret: cfg.ConnectLifeOAuthClientSecret.Value(),
		AppSecret:         cfg.ConnectLifeAppSecret.Value(),
		SignSalt:          cfg.ConnectLifeSignSalt.Value(),
	})
	if err != nil {
		return err
	}
	protocol := devicetype.Protocol{Id: cfg.PlatformProtocolId, SegmentId: cfg.PlatformProtocolSegmentId}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	appliances, err := client.GetAppliances(ctx)
	if err != nil {
		return err
	}

	switch command {
	case "login":
		fmt.Printf("login ok, %d appliances\n", len(appliances))
		return nil
	case "devices":
		return devices(appliances, asJson)
	case "status":
		a, err := pick(appliances, args, 1)
		if err != nil {
			return err
		}
		return printJson(a.StatusList)
	case "properties":
		a, err := pick(appliances, args, 1)
		if err != nil {
			return err
		}
		info, err := client.GetProperties(ctx, a.DeviceTypeCode, a.DeviceFeatureCode)
		if err != nil {
			return err
		}
		return properties(a, info, asJson)
	case "property-list":
		a, err := pick(appliances, args, 1)
		if err != nil {
			return err
		}
		raw, err := client.GetPropertyList(ctx, a.DeviceTypeCode, a.DeviceFeatureCode)
		if err != nil {
			return err
		}
		return printJson(raw)
	case "devicetype":
		a, err := pick(appliances, args, 1)
		if err != nil {
			return err
		}
		info, err := client.GetProperties(ctx, a.DeviceTypeCode, a.DeviceFeatureCode)
		if err != nil {
			return err
		}
		dt, err := devicetype.Generate(a, info, protocol)
		if err != nil {
			return err
		}
		return printJson(dt)
	case "plan":
		fs := flag.NewFlagSet("plan", flag.ContinueOnError)
		platformUrl := fs.String("platform-url", "", "platform api, e.g. the api gateway of the mgw cloud proxy; without it no lookup happens")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return plan(ctx, client, appliances, protocol, *platformUrl, asJson)
	case "set":
		a, err := pick(appliances, args, 3)
		if err != nil {
			return err
		}
		if err = client.SetProperties(ctx, a.Puid, map[string]string{args[1]: args[2]}); err != nil {
			return err
		}
		fmt.Printf("set %s=%s on %s\n", args[1], args[2], a.DeviceId)
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func pick(appliances []connectlife.Appliance, args []string, n int) (connectlife.Appliance, error) {
	if len(args) != n {
		return connectlife.Appliance{}, fmt.Errorf("expected %d argument(s)", n)
	}
	for _, a := range appliances {
		if args[0] == a.DeviceId || args[0] == a.Puid || strings.EqualFold(args[0], a.DeviceNickName) {
			return a, nil
		}
	}
	return connectlife.Appliance{}, fmt.Errorf("no appliance %q", args[0])
}

type deviceRow struct {
	DeviceId    string `json:"device_id"`
	Puid        string `json:"puid"`
	Name        string `json:"name"`
	TypeName    string `json:"type_name"`
	FeatureName string `json:"feature_name"`
	TypeKey     string `json:"type_key"`
	Online      bool   `json:"online"`
	Properties  int    `json:"properties"`
}

func devices(appliances []connectlife.Appliance, asJson bool) error {
	rows := []deviceRow{}
	for _, a := range appliances {
		rows = append(rows, deviceRow{a.DeviceId, a.Puid, a.DeviceNickName, a.DeviceTypeName, a.DeviceFeatureName, a.TypeKey(), a.Online(), len(a.StatusList)})
	}
	if asJson {
		return printJson(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE ID\tNAME\tTYPE\tMODEL\tTYPE KEY\tONLINE\tPROPERTIES")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%d\n", r.DeviceId, r.Name, r.TypeName, r.FeatureName, r.TypeKey, r.Online, r.Properties)
	}
	return w.Flush()
}

type propertyRow struct {
	Name      string `json:"name"`
	Value     any    `json:"value,omitempty"`
	Type      string `json:"type"`
	RW        string `json:"rw"`
	ValueList string `json:"value_list,omitempty"`
	Accepted  bool   `json:"accepted_name"`
}

func properties(a connectlife.Appliance, info []connectlife.PropertyInfo, asJson bool) error {
	dt, err := devicetype.Generate(a, info, devicetype.Protocol{})
	if err != nil {
		return err
	}
	types := devicetype.PropertyTypes(dt)
	byName := map[string]connectlife.PropertyInfo{}
	for _, p := range info {
		byName[p.Key] = p
	}
	names := []string{}
	for name := range types {
		names = append(names, name)
	}
	for name := range byName {
		if _, ok := types[name]; !ok {
			names = append(names, name)
		}
	}
	for name := range a.StatusList {
		if _, ok := types[name]; !ok {
			if _, ok := byName[name]; !ok {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	rows := []propertyRow{}
	for _, name := range names {
		_, accepted := types[name]
		rw := byName[name].RW
		if rw == "" {
			rw = "?"
		}
		rows = append(rows, propertyRow{
			Name:      name,
			Value:     a.StatusList[name],
			Type:      shortType(types[name]),
			RW:        rw,
			ValueList: strings.Join(strings.Fields(strings.ReplaceAll(byName[name].ValueList, "\n", "; ")), " "),
			Accepted:  accepted,
		})
	}
	if asJson {
		return printJson(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PROPERTY\tVALUE\tTYPE\tRW\tVALUES")
	for _, r := range rows {
		value := "-"
		if r.Value != nil {
			value = fmt.Sprint(r.Value)
		}
		typ := r.Type
		if !r.Accepted {
			typ = "(name rejected)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, value, typ, r.RW, r.ValueList)
	}
	return w.Flush()
}

type planRow struct {
	TypeKey      string   `json:"type_key"`
	Devices      []string `json:"devices"`
	Name         string   `json:"device_type_name,omitempty"`
	Properties   int      `json:"properties"`
	Writable     int      `json:"writable_properties"`
	Skipped      []string `json:"skipped_properties"`
	Action       string   `json:"action"`
	DeviceTypeId string   `json:"device_type_id,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// plan groups appliances by model, as the connector creates one device type per model.
func plan(ctx context.Context, client *connectlife.Client, appliances []connectlife.Appliance, protocol devicetype.Protocol, platformUrl string, asJson bool) error {
	var p *platform.Client
	if platformUrl != "" {
		p = platform.New(platformUrl)
	}
	byKey := map[string]*planRow{}
	keys := []string{}
	for _, a := range appliances {
		row, ok := byKey[a.TypeKey()]
		if ok {
			row.Devices = append(row.Devices, a.DeviceId)
			continue
		}
		row = &planRow{TypeKey: a.TypeKey(), Devices: []string{a.DeviceId}, Skipped: []string{}}
		byKey[a.TypeKey()] = row
		keys = append(keys, a.TypeKey())

		info, err := client.GetProperties(ctx, a.DeviceTypeCode, a.DeviceFeatureCode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "property list of %s: %v\n", a.TypeKey(), err)
		}
		dt, err := devicetype.Generate(a, info, protocol)
		if err != nil {
			row.Action, row.Error = "skip", err.Error()
			continue
		}
		types := devicetype.PropertyTypes(dt)
		row.Name, row.Properties, row.Writable = dt.Name, len(types), len(devicetype.Writable(info))
		for _, p := range info {
			if _, ok := types[p.Key]; !ok {
				row.Skipped = append(row.Skipped, p.Key)
			}
		}
		row.Skipped = append(row.Skipped, devicetype.MissingProperties(a.StatusList, types)...)
		row.Action = "would create (no platform lookup)"
		if p == nil {
			continue
		}
		existing, found, err := p.Find(ctx, devicetype.AttrType, a.TypeKey())
		switch {
		case err != nil:
			row.Action, row.Error = "unknown", err.Error()
		case found:
			row.Action, row.DeviceTypeId = "use existing", existing.Id
		default:
			row.Action = "would create"
		}
	}
	sort.Strings(keys)
	rows := []planRow{}
	for _, k := range keys {
		rows = append(rows, *byKey[k])
	}
	if asJson {
		return printJson(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE KEY\tDEVICE TYPE\tPROPERTIES\tWRITABLE\tSKIPPED\tACTION\tDEVICES")
	for _, r := range rows {
		action := r.Action
		if r.DeviceTypeId != "" {
			action += " " + r.DeviceTypeId
		}
		if r.Error != "" {
			action += ": " + r.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\n", r.TypeKey, r.Name, r.Properties, r.Writable, strings.Join(r.Skipped, ","), action, strings.Join(r.Devices, ","))
	}
	return w.Flush()
}

func shortType(t models.Type) string {
	switch t {
	case models.Float:
		return "Float"
	case models.String:
		return "Text"
	}
	return string(t)
}

func printJson(v any) error {
	if raw, ok := v.(json.RawMessage); ok {
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return errors.New("cloud response is not JSON")
		}
		v = parsed
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
