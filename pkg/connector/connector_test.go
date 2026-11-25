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

package connector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/devicetype"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/platform"
	"github.com/SENERGY-Platform/models/go/models"
)

// dryer is a tumble dryer as the gateway reports it: numbers as strings, one text value.
// Property names are made up; real names differ per model.
func dryer(online int) connectlife.Appliance {
	return connectlife.Appliance{
		DeviceId:          "dev1",
		Puid:              "puid1",
		DeviceNickName:    "Trockner Keller",
		DeviceTypeCode:    "030",
		DeviceFeatureCode: "1wk080027e0w",
		DeviceTypeName:    "Tumble dryer",
		OfflineState:      connectlife.FlexInt(online),
		StatusList: map[string]any{
			"program":        "3",
			"remaining_time": json.Number("95"),
			"drum_temp":      "41.5",
			"end_time":       "2026/11/25T14:30:00",
		},
	}
}

type fakeCloud struct {
	appliances   []connectlife.Appliance
	set          map[string]string
	info         []connectlife.PropertyInfo
	infoErr      error
	infoRequests int
}

func (f *fakeCloud) GetProperties(context.Context, string, string) ([]connectlife.PropertyInfo, error) {
	f.infoRequests++
	return f.info, f.infoErr
}

func (f *fakeCloud) GetAppliances(context.Context) ([]connectlife.Appliance, error) {
	return f.appliances, nil
}

func (f *fakeCloud) SetProperties(_ context.Context, puid string, properties map[string]string) error {
	f.set = properties
	f.set["puid"] = puid
	return nil
}

type event struct {
	device, service string
	value           any
}

type fakeMgw struct {
	devices []Device
	events  []event
}

func (f *fakeMgw) SetDevice(d Device) error {
	f.devices = append(f.devices, d)
	return nil
}

func (f *fakeMgw) MarshalAndSendEvent(deviceId string, serviceId string, value interface{}) error {
	f.events = append(f.events, event{deviceId, serviceId, value})
	return nil
}

// fakePlatform serves the two device-repository and device-manager endpoints the connector uses.
func fakePlatform(t *testing.T) (*httptest.Server, *[]models.DeviceType) {
	stored := &[]models.DeviceType{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /device-repository/v3/device-types", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("attr-keys") != devicetype.AttrType {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(*stored)
	})
	mux.HandleFunc("POST /device-manager/device-types", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("distinct_attributes") != devicetype.AttrType {
			t.Errorf("missing distinct_attributes: %s", r.URL.RawQuery)
		}
		var dt models.DeviceType
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &dt)
		dt.Id = "urn:infai:ses:device-type:generated"
		*stored = append(*stored, dt)
		_ = json.NewEncoder(w).Encode(dt)
	})
	return httptest.NewServer(mux), stored
}

func TestPollCreatesDeviceTypeOnceAndSendsStatus(t *testing.T) {
	server, stored := fakePlatform(t)
	defer server.Close()
	cloud := &fakeCloud{appliances: []connectlife.Appliance{dryer(1)}}
	m := &fakeMgw{}
	c := New(cloud, platform.New(server.URL), devicetype.Protocol{Id: "proto", SegmentId: "seg"}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetMgw(m)

	for i := 0; i < 2; i++ {
		if err := c.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*stored) != 1 {
		t.Fatalf("expected one created device type, got %d", len(*stored))
	}
	dt := (*stored)[0]
	if dt.Services[0].ProtocolId != "proto" || dt.Services[0].Outputs[0].ProtocolSegmentId != "seg" {
		t.Error("protocol not set")
	}
	types := devicetype.PropertyTypes(dt)
	expectedTypes := map[string]models.Type{"program": models.Float, "remaining_time": models.Float, "drum_temp": models.Float, "end_time": models.String}
	if !reflect.DeepEqual(types, expectedTypes) {
		t.Errorf("got types %v", types)
	}

	if len(m.devices) != 1 {
		t.Errorf("device must be announced once, got %d", len(m.devices))
	}
	info := m.devices[0].GetInfo()
	if info.Id != "connectlife-dev1" || info.Name != "Trockner Keller" || info.DeviceType != "urn:infai:ses:device-type:generated" || info.State != "online" {
		t.Errorf("unexpected device info %+v", info)
	}
	if len(m.events) != 2 {
		t.Fatalf("expected one status event per poll, got %d", len(m.events))
	}
	expected := map[string]any{"program": 3.0, "remaining_time": 95.0, "drum_temp": 41.5, "end_time": "2026/11/25T14:30:00"}
	if !reflect.DeepEqual(m.events[0].value, expected) || m.events[0].service != devicetype.StatusService {
		t.Errorf("unexpected event %+v", m.events[0])
	}
}

func TestPollFindsExistingDeviceType(t *testing.T) {
	server, stored := fakePlatform(t)
	defer server.Close()
	existing, err := devicetype.Generate(dryer(1), nil, devicetype.Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	existing.Id = "urn:infai:ses:device-type:existing"
	*stored = append(*stored, existing)

	m := &fakeMgw{}
	c := New(&fakeCloud{appliances: []connectlife.Appliance{dryer(1)}}, platform.New(server.URL), devicetype.Protocol{}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetMgw(m)
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*stored) != 1 || m.devices[0].DeviceTypeId != existing.Id {
		t.Errorf("existing device type not used: %d stored, device type %s", len(*stored), m.devices[0].DeviceTypeId)
	}
}

func TestOfflineAndRemovedAppliances(t *testing.T) {
	server, _ := fakePlatform(t)
	defer server.Close()
	cloud := &fakeCloud{appliances: []connectlife.Appliance{dryer(0)}}
	m := &fakeMgw{}
	c := New(cloud, platform.New(server.URL), devicetype.Protocol{}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetMgw(m)
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 0 || m.devices[0].GetInfo().State != "offline" {
		t.Error("offline appliance must be announced offline without event")
	}

	cloud.appliances = []connectlife.Appliance{dryer(1)}
	_ = c.Poll(context.Background())
	cloud.appliances = nil
	_ = c.Poll(context.Background())
	states := []string{}
	for _, d := range m.devices {
		states = append(states, string(d.GetInfo().State))
	}
	if strings.Join(states, ",") != "offline,online,offline" {
		t.Errorf("unexpected state sequence %v", states)
	}
}

func TestSetProperty(t *testing.T) {
	server, _ := fakePlatform(t)
	defer server.Close()
	cloud := &fakeCloud{appliances: []connectlife.Appliance{dryer(1)}}
	c := New(cloud, platform.New(server.URL), devicetype.Protocol{}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := &fakeMgw{}
	c.SetMgw(m)
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	var input any
	_ = json.Unmarshal([]byte(`{"property":"program","value":5}`), &input)
	if _, err := c.SetProperty(m.devices[0], input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cloud.set, map[string]string{"program": "5", "puid": "puid1"}) {
		t.Errorf("unexpected write %v", cloud.set)
	}
	status, err := c.Status(m.devices[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["program"] != 5.0 {
		t.Errorf("status must reflect the written value, got %v", status)
	}

	_ = json.Unmarshal([]byte(`{"value":5}`), &input)
	if _, err = c.SetProperty(m.devices[0], input); err == nil {
		t.Error("expected error for missing property")
	}
}

func TestMissingStatusListKeepsLastValues(t *testing.T) {
	server, stored := fakePlatform(t)
	defer server.Close()
	withoutStatus := dryer(1)
	withoutStatus.StatusList = nil
	cloud := &fakeCloud{appliances: []connectlife.Appliance{withoutStatus}}
	m := &fakeMgw{}
	c := New(cloud, platform.New(server.URL), devicetype.Protocol{}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetMgw(m)
	if err := c.Poll(context.Background()); err == nil || len(*stored) != 0 {
		t.Fatal("no device type may be created without status list and property list")
	}
	cloud.appliances = []connectlife.Appliance{dryer(1)}
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	cloud.appliances = []connectlife.Appliance{withoutStatus}
	if err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := m.events[len(m.events)-1].value.(map[string]any)
	if last["remaining_time"] != 95.0 {
		t.Errorf("last known values must be sent, got %v", last)
	}
}

func TestOfflineDryerGetsDeviceTypeFromPropertyList(t *testing.T) {
	server, stored := fakePlatform(t)
	defer server.Close()
	offline := dryer(0)
	offline.StatusList = map[string]any{}
	cloud := &fakeCloud{
		appliances: []connectlife.Appliance{offline},
		info: []connectlife.PropertyInfo{
			{Key: "Actions", RW: "RW"},
			{Key: "Selected_program_remaining_time_in_minutes", RW: "R"},
		},
	}
	m := &fakeMgw{}
	c := New(cloud, platform.New(server.URL), devicetype.Protocol{}, "connectlife-", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.SetMgw(m)
	for i := 0; i < 2; i++ {
		if err := c.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*stored) != 1 || len(devicetype.PropertyTypes((*stored)[0])) != 2 {
		t.Fatalf("expected device type with two properties, got %+v", *stored)
	}
	if cloud.infoRequests != 1 {
		t.Errorf("property list must be read once per model, got %d", cloud.infoRequests)
	}
	if m.devices[0].GetInfo().State != "offline" || len(m.events) != 0 {
		t.Error("device must be registered offline without events")
	}

	var input any
	_ = json.Unmarshal([]byte(`{"property":"Selected_program_remaining_time_in_minutes","value":5}`), &input)
	if _, err := c.SetProperty(m.devices[0], input); err == nil || err.Error() != "property Selected_program_remaining_time_in_minutes is not writable" {
		t.Errorf("read-only property must be rejected, got %v", err)
	}
	_ = json.Unmarshal([]byte(`{"property":"Actions","value":2}`), &input)
	if _, err := c.SetProperty(m.devices[0], input); err != nil || cloud.set["Actions"] != "2" {
		t.Errorf("writable property must pass, got %v %v", err, cloud.set)
	}
}
