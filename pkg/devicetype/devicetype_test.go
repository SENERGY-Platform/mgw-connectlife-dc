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
	"github.com/SENERGY-Platform/models/go/models"
)

func TestGenerateSkipsInvalidNames(t *testing.T) {
	dt, err := Generate(connectlife.Appliance{StatusList: map[string]any{
		"Dry_Level": "2", "1st": "x", "a.b": "1", "with space": "1", "mode-2": "a",
	}}, nil, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]models.Type{"Dry_Level": models.Float, "mode-2": models.String}
	if actual := PropertyTypes(dt); !reflect.DeepEqual(actual, expected) {
		t.Errorf("got %v", actual)
	}
}

func TestGenerateRejectsEmptyStatus(t *testing.T) {
	if _, err := Generate(connectlife.Appliance{}, nil, Protocol{}); err == nil {
		t.Error("expected error for empty status list")
	}
	if _, err := Generate(connectlife.Appliance{StatusList: map[string]any{"9x": "1"}}, nil, Protocol{}); err == nil {
		t.Error("expected error without usable property")
	}
}

func TestGenerateFromPropertyListWhileOffline(t *testing.T) {
	info := []connectlife.PropertyInfo{
		{Key: "Actions", RW: "RW"},
		{Key: "Selected_program_remaining_time_in_minutes", RW: "R"},
		{Key: "Child_lock", RW: "RW"},
	}
	dt, err := Generate(connectlife.Appliance{DeviceTypeCode: "030", DeviceFeatureCode: "m"}, info, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]models.Type{"Actions": models.Float, "Selected_program_remaining_time_in_minutes": models.Float, "Child_lock": models.Float}
	if actual := PropertyTypes(dt); !reflect.DeepEqual(actual, expected) {
		t.Errorf("got %v", actual)
	}
	if d := dt.Services[1].Description; d != "Writes one property. Writable: Actions, Child_lock." {
		t.Errorf("unexpected description %q", d)
	}
}

func TestGenerateMergesStatusTypes(t *testing.T) {
	info := []connectlife.PropertyInfo{{Key: "version", RW: "R"}, {Key: "mode", RW: "RW"}}
	dt, err := Generate(connectlife.Appliance{StatusList: map[string]any{"version": "1.2.3", "extra": "4"}}, info, Protocol{})
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]models.Type{"version": models.String, "mode": models.Float, "extra": models.Float}
	if actual := PropertyTypes(dt); !reflect.DeepEqual(actual, expected) {
		t.Errorf("got %v", actual)
	}
}

func TestStatusEventDropsValuesOfWrongType(t *testing.T) {
	types := map[string]models.Type{"power": models.Float, "label": models.String}
	actual := StatusEvent(map[string]any{"power": "OFF", "label": 3, "unknown": "1"}, types)
	expected := map[string]any{"label": "3"}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("got %v", actual)
	}
}

func TestDisplayNameFallsBackToTypeCode(t *testing.T) {
	a := connectlife.Appliance{DeviceTypeCode: "030", DeviceFeatureCode: "1wk080066v0w", DeviceFeatureName: "DH3S80-DVW001"}
	if actual := displayName(a); actual != "ConnectLife Tumble dryer DH3S80-DVW001 (030-1wk080066v0w)" {
		t.Errorf("got %q", actual)
	}
}
