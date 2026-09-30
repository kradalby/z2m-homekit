package devices

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"tailscale.com/types/opt"
)

// fullReading sets every Reading field to a non-zero value, via reflection so
// a newly added field is covered without touching this test.
func fullReading(t *testing.T) Reading {
	t.Helper()

	var r Reading
	rv := reflect.ValueOf(&r).Elem()
	for i := range rv.NumField() {
		set := rv.Field(i).Addr().MethodByName("Set")
		arg := reflect.New(set.Type().In(0)).Elem()
		switch arg.Kind() {
		case reflect.Bool:
			arg.SetBool(true)
		case reflect.Int:
			arg.SetInt(7)
		case reflect.Float64:
			arg.SetFloat(1.5)
		default:
			t.Fatalf("Reading.%s: unhandled kind %s", rv.Type().Field(i).Name, arg.Kind())
		}
		set.Call([]reflect.Value{arg})
	}

	return r
}

// Every field a report carries must survive the merge; a field missing from
// Apply would silently never reach HomeKit or the UI.
func TestApplyMergesEveryField(t *testing.T) {
	full := fullReading(t)
	seen := time.Unix(1000, 0)

	got := Apply(State{}, full, seen)
	if got.Reading != full {
		t.Errorf("Apply dropped fields:\n got %+v\nwant %+v", got.Reading, full)
	}
	if !got.LastSeen.Equal(seen) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, seen)
	}
}

// Z2M sends partial reports; fields absent from one must keep their value.
func TestApplyKeepsUnreportedFields(t *testing.T) {
	prev := State{Reading: fullReading(t)}

	got := Apply(prev, Reading{}, time.Unix(2000, 0))
	if got.Reading != prev.Reading {
		t.Errorf("empty reading changed state:\n got %+v\nwant %+v", got.Reading, prev.Reading)
	}

	var r Reading
	r.Temperature.Set(21.5)
	got = Apply(prev, r, time.Unix(3000, 0))

	want := prev.Reading
	want.Temperature = opt.ValueOf(21.5)
	if got.Reading != want {
		t.Errorf("partial reading:\n got %+v\nwant %+v", got.Reading, want)
	}
}

func TestParseZ2M(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want func(*Reading)
	}{
		{
			name: "climate",
			msg:  `{"temperature":21.5,"humidity":40,"battery":90,"pressure":1013.2,"linkquality":120}`,
			want: func(r *Reading) {
				r.Temperature.Set(21.5)
				r.Humidity.Set(40)
				r.Battery.Set(90)
				r.Pressure.Set(1013.2)
				r.LinkQuality.Set(120)
			},
		},
		{
			name: "binary sensors",
			msg:  `{"occupancy":true,"contact":false,"water_leak":true,"smoke":false,"tamper":true}`,
			want: func(r *Reading) {
				r.Occupancy.Set(true)
				r.Contact.Set(false)
				r.WaterLeak.Set(true)
				r.Smoke.Set(false)
				r.Tamper.Set(true)
			},
		},
		{
			name: "illuminance_lux wins over illuminance",
			msg:  `{"illuminance":10,"illuminance_lux":200}`,
			want: func(r *Reading) { r.Illuminance.Set(200) },
		},
		{
			name: "light",
			msg:  `{"state":"ON","brightness":254,"color_temp":300,"color":{"hue":120,"saturation":50}}`,
			want: func(r *Reading) {
				r.On.Set(true)
				r.Brightness.Set(254)
				r.ColorTemp.Set(300)
				r.Hue.Set(120)
				r.Saturation.Set(50)
			},
		},
		{
			name: "fan mode maps to speed",
			msg:  `{"fan_state":"OFF","fan_speed":10,"fan_mode":"high"}`,
			want: func(r *Reading) {
				r.On.Set(false)
				r.FanSpeed.Set(100)
			},
		},
		{
			name: "nothing we track",
			msg:  `{"update":{"state":"idle"},"voltage":3000}`,
			want: func(*Reading) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg map[string]any
			if err := json.Unmarshal([]byte(tt.msg), &msg); err != nil {
				t.Fatal(err)
			}

			var want Reading
			tt.want(&want)

			if got := ParseZ2M(msg); got != want {
				t.Errorf("ParseZ2M(%s):\n got %+v\nwant %+v", tt.msg, got, want)
			}
		})
	}
}
