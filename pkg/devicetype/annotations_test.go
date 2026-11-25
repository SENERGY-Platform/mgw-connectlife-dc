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
	"reflect"
	"testing"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
)

func TestParseValueList(t *testing.T) {
	cases := map[string]map[string]string{
		"0:reserved,1:poweroff,2:startrun,3:pause": {"reserved": "0", "poweroff": "1", "startrun": "2", "pause": "3"},
		"0: Disable\n1: Enable":                    {"disable": "0", "enable": "1"},
		"0：English（欧洲）;1：English（澳洲）":              {"english（欧洲）": "0", "english（澳洲）": "1"},
		"SelectedProgramId":                        {},
	}
	for list, expected := range cases {
		if actual := ParseValueList(list); !reflect.DeepEqual(actual, expected) {
			t.Errorf("%q: got %v", list, actual)
		}
	}
}

func TestDryerAnnotations(t *testing.T) {
	info := []connectlife.PropertyInfo{
		{Key: "Actions", RW: "RW", ValueList: "0:reserved,1:poweroff,2:startrun,3:pause"},
		{Key: "Selected_program_remaining_time_in_minutes", RW: "R"},
	}
	dt, err := Generate(connectlife.Appliance{DeviceTypeCode: "030", DeviceFeatureCode: "m"}, info, Protocol{Id: "p", SegmentId: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if dt.DeviceClassId != deviceClassTumbleDryer {
		t.Errorf("device class %q", dt.DeviceClassId)
	}
	ids := []string{}
	for _, s := range dt.Services {
		ids = append(ids, s.LocalId)
	}
	if !reflect.DeepEqual(ids, []string{"status", "set_property", "start", "pause", "power_off"}) {
		t.Fatalf("got services %v", ids)
	}
	start := dt.Services[2].Inputs[0].ContentVariable.SubContentVariables
	if start[0].Value != "Actions" || start[1].Value != "2" || start[1].FunctionId != functionSetStart {
		t.Errorf("unexpected start input %+v", start)
	}
	for _, v := range dt.Services[0].Outputs[0].ContentVariable.SubContentVariables {
		if v.Name == "Selected_program_remaining_time_in_minutes" && (v.FunctionId != functionGetDuration || v.CharacteristicId != characteristicMinutes) {
			t.Errorf("remaining time not annotated: %+v", v)
		}
		if v.Name == "Actions" && v.FunctionId != "" {
			t.Error("Actions must stay unannotated in the status")
		}
	}
}

func TestNoActionServicesWithoutWritableActions(t *testing.T) {
	info := []connectlife.PropertyInfo{{Key: "Actions", RW: "R", ValueList: "2:startrun"}}
	dt, err := Generate(connectlife.Appliance{}, info, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dt.Services) != 2 {
		t.Errorf("got %d services", len(dt.Services))
	}
}
