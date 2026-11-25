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

package devicetype

import (
	"regexp"
	"strings"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
	"github.com/SENERGY-Platform/models/go/models"
)

// Ontology ids of the SENERGY platform, read on 2026-11-25.
const (
	aspectDevice = "urn:infai:ses:aspect:861227f6-1523-46a7-b8ab-a4e76f0bdd32"

	functionGetDuration   = "urn:infai:ses:measuring-function:5692b87e-5377-4738-be33-762322fdc62e"
	characteristicMinutes = "urn:infai:ses:characteristic:b36eee5d-52f0-4476-a6f7-6dd03b24e0f8"

	functionSetStart     = "urn:infai:ses:controlling-function:1664d813-e30d-4f57-a314-aa53925f3dc4"
	functionSetPause     = "urn:infai:ses:controlling-function:6cf43287-a134-4b53-9377-630aea47142b"
	functionSetDeviceOff = "urn:infai:ses:controlling-function:2f35150b-9df7-4cad-95bc-165fa00219fd"
)

type statusAnnotation struct {
	function       string
	characteristic string
}

// statusAnnotations annotates status properties whose meaning and unit are certain from the
// property name. Energy values stay unannotated: their unit is not documented.
var statusAnnotations = map[string]statusAnnotation{
	"Selected_program_remaining_time_in_minutes": {functionGetDuration, characteristicMinutes},
}

func annotate(v models.ContentVariable) models.ContentVariable {
	if a, ok := statusAnnotations[v.Name]; ok {
		v.FunctionId = a.function
		v.CharacteristicId = a.characteristic
		v.AspectId = aspectDevice
		v.AspectIds = []string{aspectDevice}
	}
	return v
}

// actionProperty starts, pauses and switches off laundry appliances; its value list names the codes.
const actionProperty = "Actions"

type actionService struct {
	localId  string
	name     string
	label    string // label in the value list of the Actions property
	function string
}

var actionServices = []actionService{
	{"start", "Start", "startrun", functionSetStart},
	{"pause", "Pause", "pause", functionSetPause},
	{"power_off", "Power Off", "poweroff", functionSetDeviceOff},
}

// ActionServiceIds are the local ids of all action services; the connector handles them like set_property.
func ActionServiceIds() []string {
	ids := []string{}
	for _, a := range actionServices {
		ids = append(ids, a.localId)
	}
	return ids
}

// actionCommands builds one command service per action the Actions property offers.
// Each service writes a fixed value, so it carries a controlling function without concept.
func actionCommands(info []connectlife.PropertyInfo, protocol Protocol) []models.Service {
	var actions connectlife.PropertyInfo
	for _, p := range info {
		if p.Key == actionProperty && p.Writable() {
			actions = p
		}
	}
	codes := ParseValueList(actions.ValueList)
	result := []models.Service{}
	for _, a := range actionServices {
		code, ok := codes[a.label]
		if !ok {
			continue
		}
		result = append(result, models.Service{
			LocalId:     a.localId,
			Name:        a.name,
			Description: "Writes " + actionProperty + "=" + code + ".",
			Interaction: models.REQUEST,
			ProtocolId:  protocol.Id,
			Inputs: []models.Content{{
				ContentVariable: models.ContentVariable{
					Name: "command",
					Type: models.Structure,
					SubContentVariables: []models.ContentVariable{
						{Name: "property", Type: models.String, Value: actionProperty},
						{Name: "value", Type: models.String, Value: code, FunctionId: a.function, AspectIds: []string{aspectDevice}},
					},
				},
				Serialization:     models.JSON,
				ProtocolSegmentId: protocol.SegmentId,
			}},
		})
	}
	return result
}

var valueListEntry = regexp.MustCompile(`(\d+)\s*[:：]\s*([^,;\n]+)`)

// ParseValueList reads lists like "0:reserved,1:poweroff,2:startrun" into label -> code,
// labels lower case and without spaces.
func ParseValueList(list string) map[string]string {
	result := map[string]string{}
	for _, m := range valueListEntry.FindAllStringSubmatch(list, -1) {
		label := strings.ToLower(strings.Join(strings.Fields(m[2]), ""))
		if _, exists := result[label]; !exists {
			result[label] = m[1]
		}
	}
	return result
}

const deviceClassTumbleDryer = "urn:infai:ses:device-class:f2bb947a-d71f-4069-9d97-57a434e86efd"

// deviceClasses assigns the platform device class by ConnectLife type code.
var deviceClasses = map[string]string{
	"004": deviceClassTumbleDryer,
	"030": deviceClassTumbleDryer,
	"032": deviceClassTumbleDryer,
}
