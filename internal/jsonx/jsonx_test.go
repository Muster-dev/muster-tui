package jsonx

import "testing"

func TestRoundTripPreservesOrderAndUnknownKeys(t *testing.T) {
	in := `{
  "project": "acme",
  "zeta": {"x": [1, 2, {"y": null}]},
  "services": {
    "web": {"name": "Web", "health": {"enabled": false}},
    "api": {"name": "API", "custom": true}
  },
  "deploy_order": ["web", "api"]
}`
	o, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	o.Set(8080, "services", "api", "health", "port")
	o.Set("http", "services", "api", "health", "type")
	out, err := o.Pretty()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "project": "acme",
  "zeta": {
    "x": [
      1,
      2,
      {
        "y": null
      }
    ]
  },
  "services": {
    "web": {
      "name": "Web",
      "health": {
        "enabled": false
      }
    },
    "api": {
      "name": "API",
      "custom": true,
      "health": {
        "port": 8080,
        "type": "http"
      }
    }
  },
  "deploy_order": [
    "web",
    "api"
  ]
}
`
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if n, ok := o.Int("services", "api", "health", "port"); !ok || n != 8080 {
		t.Fatalf("Int = %d %v", n, ok)
	}
	if got := o.Strings("deploy_order"); len(got) != 2 || got[1] != "api" {
		t.Fatalf("Strings = %v", got)
	}
	o.Delete("services", "web")
	if o.Obj("services").Len() != 1 {
		t.Fatal("delete failed")
	}
}

func TestParseRejectsNonObject(t *testing.T) {
	if _, err := Parse([]byte(`[1]`)); err == nil {
		t.Fatal("expected error")
	}
	if _, err := Parse([]byte(`{"a":1} x`)); err == nil {
		t.Fatal("expected error for trailing data")
	}
}
