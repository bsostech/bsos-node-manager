package server

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"besu-manager/backend/internal/api"
)

func TestNormSources(t *testing.T) {
	ptr := func(v ...string) *[]string { return &v }
	cases := []struct {
		name    string
		in      *[]string
		want    []string
		wantErr bool
	}{
		{"沒帶 → 空清單", nil, []string{}, false},
		{"單一 IP 補成 /32", ptr("10.0.0.31"), []string{"10.0.0.31/32"}, false},
		{"網段正規化到網段位址", ptr("10.0.0.77/24"), []string{"10.0.0.0/24"}, false},
		{"IPv6 補成 /128", ptr("2001:db8::5"), []string{"2001:db8::5/128"}, false},
		{"去重、保持順序、略過空白項", ptr(" 10.0.0.2 ", "", "10.0.0.1", "10.0.0.2/32"), []string{"10.0.0.2/32", "10.0.0.1/32"}, false},
		{"主機名不收", ptr("node-a3.example"), nil, true},
		{"一項塞兩個(逗號)不收", ptr("10.0.0.1,10.0.0.2"), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, msg := normSources(c.in)
			if (msg != nil) != c.wantErr || (msg != nil && (!hasReason(*msg, api.ErrorReasonAllowedSourceInvalid) || (*msg.Params)["value"] != strconv.Quote((*c.in)[0]))) {
				t.Fatalf("normSources() = %#v, wantErr %v", msg, c.wantErr)
			}
			if !c.wantErr && !reflect.DeepEqual(got, c.want) {
				t.Errorf("normSources() = %v, want %v", got, c.want)
			}
		})
	}

	many := make([]string, maxAllowedSources+1)
	for i := range many {
		many[i] = fmt.Sprintf("10.0.%d.1", i)
	}
	if got, msg := normSources(new(many[:maxAllowedSources])); msg != nil || len(got) != maxAllowedSources {
		t.Errorf("剛好 %d 項應通過:%d 項、%#v", maxAllowedSources, len(got), msg)
	}
	if _, msg := normSources(&many); msg == nil || !hasReason(*msg, api.ErrorReasonAllowedSourcesTooMany) || (*msg.Params)["limit"] != strconv.Itoa(maxAllowedSources) {
		t.Errorf("超過 %d 項應拒絕", maxAllowedSources)
	}
}
