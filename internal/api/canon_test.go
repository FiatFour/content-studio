package api_test

import (
	"fmt"
	"strings"
	"testing"
)

const mia = `{"slug":"mia","name":"Mia","appearance":"short black hair, yellow hoodie","personality":"curious","voice":"bright, fast talker","arrives_on":"2020-01-01"}`

func TestCharacterCreateListUpdate(t *testing.T) {
	ts := server(t)
	if code, body := call(t, ts, "GET", "/api/characters", "", ""); code != 200 || len(body["characters"].([]any)) != 0 {
		t.Fatalf("empty list: %d %v", code, body)
	}
	code, body := call(t, ts, "POST", "/api/characters", "", mia)
	if code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	id := int64(body["id"].(float64))
	_, list := call(t, ts, "GET", "/api/characters", "", "")
	c := list["characters"].([]any)[0].(map[string]any)
	if c["slug"] != "mia" || c["status"] != "alive" || c["arrives_on"] != "2020-01-01" || c["status_since"] != nil || c["lora_name"] != "" {
		t.Fatalf("created %v", c)
	}
	upd := `{"name":"Mia Lee","appearance":"short black hair, green hoodie","voice":"bright, fast talker","status":"moved_away","status_since":"2024-06-01","lora_name":"mia_v1","trigger_token":"miachar"}`
	if code, body := call(t, ts, "PUT", fmt.Sprintf("/api/characters/%d", id), "", upd); code != 200 {
		t.Fatalf("update %d %v", code, body)
	}
	_, list = call(t, ts, "GET", "/api/characters", "", "")
	c = list["characters"].([]any)[0].(map[string]any)
	if c["name"] != "Mia Lee" || c["status"] != "moved_away" || c["status_since"] != "2024-06-01" || c["lora_name"] != "mia_v1" || c["trigger_token"] != "miachar" || c["slug"] != "mia" {
		t.Fatalf("updated %v", c)
	}
	// Setting the character back to alive drops the status date.
	back := `{"name":"Mia Lee","appearance":"short black hair, green hoodie","voice":"bright, fast talker","status":"alive","status_since":"2024-06-01"}`
	call(t, ts, "PUT", fmt.Sprintf("/api/characters/%d", id), "", back)
	_, list = call(t, ts, "GET", "/api/characters", "", "")
	if c = list["characters"].([]any)[0].(map[string]any); c["status"] != "alive" || c["status_since"] != nil {
		t.Fatalf("alive again %v", c)
	}
	if code, _ := call(t, ts, "PUT", "/api/characters/999", "", back); code != 404 {
		t.Fatalf("unknown id: %d", code)
	}
	if code, _ := call(t, ts, "PUT", fmt.Sprintf("/api/characters/%d", id), "", `{"slug":"other",`+back[1:]); code != 400 {
		t.Fatalf("slug change: %d", code)
	}
}

func TestCharacterValidation(t *testing.T) {
	ts := server(t)
	long := func(n int) string { return strings.Repeat("ก", n) }
	mk := func(over string) string { // later keys win in encoding/json
		return mia[:len(mia)-1] + "," + over + "}"
	}
	cases := map[string]string{
		"slug uppercase":      mk(`"slug":"Mia"`),
		"slug digit first":    mk(`"slug":"1mia"`),
		"slug too long":       mk(`"slug":"abcdefghijklmnopq"`),
		"slug dash":           mk(`"slug":"mi-a"`),
		"empty name":          mk(`"name":"  "`),
		"name 61":             mk(`"name":"` + long(61) + `"`),
		"appearance 4":        mk(`"appearance":"abcd"`),
		"appearance 601":      mk(`"appearance":"` + long(601) + `"`),
		"personality 601":     mk(`"personality":"` + long(601) + `"`),
		"voice 4":             mk(`"voice":"abcd"`),
		"voice 201":           mk(`"voice":"` + long(201) + `"`),
		"bad status":          mk(`"status":"missing"`),
		"deceased no date":    mk(`"status":"deceased"`),
		"moved_away no date":  mk(`"status":"moved_away"`),
		"bad arrives_on":      mk(`"arrives_on":"2020-13-40"`),
		"bad status_since":    mk(`"status":"deceased","status_since":"yesterday"`),
		"lora path traversal": mk(`"lora_name":"../x"`),
		"lora space":          mk(`"lora_name":"a b"`),
		"lora 101":            mk(`"lora_name":"` + strings.Repeat("a", 101) + `"`),
		"trigger 61":          mk(`"trigger_token":"` + strings.Repeat("a", 61) + `"`),
		"not json":            `{"slug":`,
	}
	for name, body := range cases {
		code, out := call(t, ts, "POST", "/api/characters", "", body)
		if code != 400 {
			t.Errorf("%s: got %d %v", name, code, out)
		} else if msg, _ := out["error"].(string); msg == "" || strings.Contains(msg, "SQLSTATE") || strings.Contains(msg, "violates") {
			t.Errorf("%s: unsafe message %q", name, msg)
		}
	}
	// Boundary values are accepted.
	ok := mk(`"slug":"a234567890123456","name":"` + long(60) + `","appearance":"` + long(600) + `","personality":"` + long(600) + `","voice":"` + long(200) + `","lora_name":"` + strings.Repeat("a", 100) + `","trigger_token":"` + strings.Repeat("a", 60) + `"`)
	if code, out := call(t, ts, "POST", "/api/characters", "", ok); code != 201 {
		t.Fatalf("boundary values rejected: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", "/api/characters", "", mk(`"slug":"gone","status":"deceased","status_since":"2023-05-05"`)); code != 201 {
		t.Fatalf("deceased with date rejected: %d %v", code, out)
	}
}

func TestDuplicateCharacterSlugIs409(t *testing.T) {
	ts := server(t)
	if code, _ := call(t, ts, "POST", "/api/characters", "", mia); code != 201 {
		t.Fatal("first create failed")
	}
	code, out := call(t, ts, "POST", "/api/characters", "", mia)
	if code != 409 || strings.Contains(out["error"].(string), "SQLSTATE") {
		t.Fatalf("%d %v", code, out)
	}
}

func TestEpisodeSummaryRoundTrip(t *testing.T) {
	ts := server(t)
	call(t, ts, "POST", "/api/characters", "", mia)
	code, body := call(t, ts, "POST", "/api/episodes", "", `{"title":"ep","idea":"long enough idea here","target_duration_s":60,"in_story_date":"2024-03-01"}`)
	if code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	path := fmt.Sprintf("/api/episodes/%d", int64(body["episode_id"].(float64)))
	_, got := call(t, ts, "GET", path, "", "")
	ep := got["episode"].(map[string]any)
	if ep["in_story_date"] != "2024-03-01" || got["summary"] != nil {
		t.Fatalf("before: %v", got)
	}
	sum := `{"summary":"Mia loses her keys.","characters_present":["mia","mia"],"open_threads":["who has the keys"," ",""]}`
	if code, out := call(t, ts, "PUT", path+"/summary", "", sum); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	_, got = call(t, ts, "GET", path, "", "")
	s := got["summary"].(map[string]any)
	if s["summary"] != "Mia loses her keys." || fmt.Sprint(s["characters_present"]) != "[mia]" || fmt.Sprint(s["open_threads"]) != "[who has the keys]" {
		t.Fatalf("after: %v", s)
	}
	// Second put replaces, it does not append.
	call(t, ts, "PUT", path+"/summary", "", `{"summary":"Changed.","characters_present":[],"open_threads":[]}`)
	_, got = call(t, ts, "GET", path, "", "")
	if s = got["summary"].(map[string]any); s["summary"] != "Changed." || len(s["characters_present"].([]any)) != 0 || len(s["open_threads"].([]any)) != 0 {
		t.Fatalf("replaced: %v", s)
	}
	// In-story date can be changed and cleared.
	if code, _ := call(t, ts, "PUT", path+"/in-story-date", "", `{"in_story_date":"2025-01-02"}`); code != 200 {
		t.Fatal("set date")
	}
	_, got = call(t, ts, "GET", path, "", "")
	if got["episode"].(map[string]any)["in_story_date"] != "2025-01-02" {
		t.Fatalf("date %v", got["episode"])
	}
	call(t, ts, "PUT", path+"/in-story-date", "", `{"in_story_date":""}`)
	_, got = call(t, ts, "GET", path, "", "")
	if got["episode"].(map[string]any)["in_story_date"] != nil {
		t.Fatalf("date not cleared %v", got["episode"])
	}
}

func TestEpisodeSummaryValidation(t *testing.T) {
	ts := server(t)
	call(t, ts, "POST", "/api/characters", "", mia)
	_, body := call(t, ts, "POST", "/api/episodes", "", `{"title":"ep","idea":"long enough idea here","target_duration_s":60}`)
	path := fmt.Sprintf("/api/episodes/%d", int64(body["episode_id"].(float64)))
	threads := func(n, size int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `"` + strings.Repeat("x", size) + `"`
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	bad := map[string]string{
		"empty summary":    `{"summary":" ","characters_present":[],"open_threads":[]}`,
		"summary 2001":     `{"summary":"` + strings.Repeat("a", 2001) + `","characters_present":[],"open_threads":[]}`,
		"unknown present":  `{"summary":"ok","characters_present":["nobody"],"open_threads":[]}`,
		"11 threads":       `{"summary":"ok","characters_present":[],"open_threads":` + threads(11, 5) + `}`,
		"thread 201 chars": `{"summary":"ok","characters_present":[],"open_threads":` + threads(1, 201) + `}`,
	}
	for name, b := range bad {
		if code, out := call(t, ts, "PUT", path+"/summary", "", b); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	good := `{"summary":"` + strings.Repeat("a", 2000) + `","characters_present":["mia"],"open_threads":` + threads(10, 200) + `}`
	if code, out := call(t, ts, "PUT", path+"/summary", "", good); code != 200 {
		t.Fatalf("boundary: %d %v", code, out)
	}
	if code, _ := call(t, ts, "PUT", "/api/episodes/999/summary", "", `{"summary":"ok"}`); code != 404 {
		t.Fatalf("unknown episode: %d", code)
	}
	if code, _ := call(t, ts, "PUT", path+"/in-story-date", "", `{"in_story_date":"2024-02-30"}`); code != 400 {
		t.Fatalf("bad date: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/episodes", "", `{"title":"ep","idea":"long enough idea here","target_duration_s":60,"in_story_date":"soon"}`); code != 400 {
		t.Fatalf("bad create date: %d", code)
	}
}
