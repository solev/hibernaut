package hibapi_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestM10FieldsRoundTrip(t *testing.T) {
	machines := 3
	want := hibapi.Response{
		V:        hibapi.Version,
		Event:    &hibapi.Event{Image: "dev", Size: "small", Step: "4/12", Text: "RUN npm i -g @openai/codex", State: hibapi.EventBuilt, Seconds: 41},
		Features: []string{hibapi.FeatureBuildEvents, hibapi.FeatureDisconnect},
		Host:     &hibapi.Host{State: hibapi.HostAsleep, Detail: "VM deallocated"},
		Status:   &hibapi.Status{Version: "0.5.0", E2B: hibapi.Health{OK: true}, Machines: &machines},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := hibapi.DecodeResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	for _, field := range []string{
		`"event":{"image":"dev","size":"small","step":"4/12","text":"RUN npm i -g @openai/codex","state":"built","seconds":41}`,
		`"features":["build-events","disconnect"]`,
		`"host":{"state":"asleep","detail":"VM deallocated"}`,
		`"machines":3`,
	} {
		if !bytes.Contains(data, []byte(field)) {
			t.Errorf("JSON %s lacks %s", data, field)
		}
	}
}

func TestEventStates(t *testing.T) {
	for state, want := range map[hibapi.EventState]string{
		hibapi.EventStarted: "started", hibapi.EventReused: "reused", hibapi.EventBuilt: "built", hibapi.EventFailed: "failed",
	} {
		if string(state) != want {
			t.Errorf("state %q, want %q", state, want)
		}
	}
	data, err := json.Marshal(hibapi.Event{Image: "dev", Step: "base", Text: "FROM e2bdev/base", State: hibapi.EventStarted})
	if want := `{"image":"dev","step":"base","text":"FROM e2bdev/base","state":"started"}`; err != nil || string(data) != want {
		t.Errorf("started event = %s, %v; want %s", data, err, want)
	}
}

func TestFeaturesAreSentOnlyWhenListed(t *testing.T) {
	for _, tc := range []struct {
		features []string
		want     string
	}{
		{nil, `{"v":1,"version":"0.5.0"}`},
		{[]string{}, `{"v":1,"version":"0.5.0","features":[]}`},
		{[]string{hibapi.FeatureBuildEvents}, `{"v":1,"version":"0.5.0","features":["build-events"]}`},
	} {
		data, err := json.Marshal(hibapi.Response{V: hibapi.Version, Version: "0.5.0", Features: tc.features})
		if err != nil || string(data) != tc.want {
			t.Errorf("features %#v: %s, %v; want %s", tc.features, data, err, tc.want)
		}
	}
}

func TestOlderRepliesDecodeWithoutM10Fields(t *testing.T) {
	r, err := hibapi.DecodeResponse([]byte(`{"v":1,"version":"0.4.0"}`))
	if err != nil || r.Version != "0.4.0" || r.Features != nil || r.Event != nil || r.Host != nil {
		t.Errorf("older version reply = %+v, %v", r, err)
	}
	r, err = hibapi.DecodeResponse([]byte(`{"v":1,"status":{"version":"0.4.0","e2b":{"ok":true},"secrets":[]}}`))
	if err != nil || r.Status == nil || r.Status.Machines != nil || !r.Status.OK() {
		t.Errorf("older status reply = %+v, %v; want no machine count", r, err)
	}
	r, err = hibapi.DecodeResponse([]byte(`{"v":1,"status":{"version":"0.5.0","e2b":{"ok":true},"secrets":[],"machines":0}}`))
	if err != nil || r.Status == nil || r.Status.Machines == nil || *r.Status.Machines != 0 {
		t.Errorf("status with no machines = %+v, %v; want a count of 0", r, err)
	}
	data, err := json.Marshal(hibapi.Status{Version: "0.5.0"})
	if err != nil || bytes.Contains(data, []byte("machines")) {
		t.Errorf("status without a count = %s, %v; want no machines field", data, err)
	}
}

func TestHostStates(t *testing.T) {
	data, err := json.Marshal(hibapi.Response{V: hibapi.Version, Host: &hibapi.Host{State: hibapi.HostRunning}})
	if want := `{"v":1,"host":{"state":"running"}}`; err != nil || string(data) != want {
		t.Errorf("running host = %s, %v; want %s", data, err, want)
	}
}
