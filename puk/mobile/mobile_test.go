package mobile

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/welkin-moon/sing-box/puk/model"
)

type protector struct{ calls atomic.Int32 }

func (p *protector) Protect(fd int64) bool { p.calls.Add(1); return false }
func TestRequiresProtectionAndPropagatesDenial(t *testing.T) {
	cfg := model.Config{Controller: "http://127.0.0.1:1", Policies: []model.Policy{{Selector: "test", Candidates: []model.Candidate{{Tag: "a", ProxyURL: "http://127.0.0.1:1"}}, Probes: []model.Probe{{URL: "https://example.com", Statuses: []int{200}}}}}}
	raw, _ := json.Marshal(cfg)
	if _, e := NewController(string(raw), nil); e == nil {
		t.Fatal("missing protector accepted")
	}
	p := &protector{}
	c, e := NewController(string(raw), p)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Probe(); e == nil {
		t.Fatal("denied socket protection must fail")
	}
	if p.calls.Load() == 0 {
		t.Fatal("protector not called")
	}
	c.Stop() // also safe before Start
}
