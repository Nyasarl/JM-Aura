package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// readerSettings 是阅读器设置接口的响应形状（服务端只做区间钳制）。
type readerSettingsOut struct {
	Width     int `json:"width"`
	Gap       int `json:"gap"`
	Initial   int `json:"initial"`
	Batch     int `json:"batch"`
	KeepPages int `json:"keep_pages"`
}

func (f *communityFixture) readerSettings(t *testing.T, sid string) (int, *readerSettingsOut) {
	t.Helper()
	_, env := f.call(http.MethodGet, "/api/aura/reader/settings", "", sid)
	if env.St != StatusOK {
		return env.St, nil
	}
	if string(env.Data) == "null" {
		return env.St, nil
	}
	var s readerSettingsOut
	if err := json.Unmarshal(env.Data, &s); err != nil {
		t.Fatalf("settings data %q: %v", string(env.Data), err)
	}
	return env.St, &s
}

// TestReaderSettingsRequiresLogin 保证设置只会写进「有账号」的用户记录里：
// 访客没有可存的桶，必须被拒，否则会写进空用户名。
func TestReaderSettingsRequiresLogin(t *testing.T) {
	f := newCommunityFixture(t)
	if _, env := f.call(http.MethodGet, "/api/aura/reader/settings", "", ""); env.St != StatusNotLogin {
		t.Fatalf("anonymous GET st=%d, want %d", env.St, StatusNotLogin)
	}
	body := `{"keep_pages":3}`
	if rec, env := f.call(http.MethodPut, "/api/aura/reader/settings", body, ""); rec.Code != http.StatusUnauthorized && env.St == StatusOK {
		t.Fatalf("anonymous PUT should be refused, got st=%d", env.St)
	}
}

// TestReaderSettingsRoundTripAndClamp 覆盖读写、越界钳制、局部更新不清字段、用户隔离。
func TestReaderSettingsRoundTripAndClamp(t *testing.T) {
	f := newCommunityFixture(t)
	alice, bob := f.sid(t, "alice"), f.sid(t, "bob")

	// 从未保存过 → null（前端继续用自己的默认值）。
	if _, s := f.readerSettings(t, alice); s != nil {
		t.Fatalf("fresh user should have no settings, got %#v", s)
	}

	// 完整保存 + 读回。
	full := `{"width":80,"gap":4,"initial":5,"batch":2,"keep_pages":3}`
	if _, env := f.call(http.MethodPut, "/api/aura/reader/settings", full, alice); env.St != StatusOK {
		t.Fatalf("put st=%d msg=%s", env.St, env.Msg)
	}
	if _, s := f.readerSettings(t, alice); s == nil || *s != (readerSettingsOut{80, 4, 5, 2, 3}) {
		t.Fatalf("round trip = %#v", s)
	}

	// 越界值被钳制：保留页数必须落在 1..4（直接决定同时持有的画布数量）。
	out := `{"width":1,"gap":999,"initial":0,"batch":42,"keep_pages":99}`
	if _, env := f.call(http.MethodPut, "/api/aura/reader/settings", out, alice); env.St != StatusOK {
		t.Fatalf("clamp put st=%d msg=%s", env.St, env.Msg)
	}
	if _, s := f.readerSettings(t, alice); s == nil || *s != (readerSettingsOut{20, 50, 1, 6, 4}) {
		t.Fatalf("clamped = %#v", s)
	}
	if _, env := f.call(http.MethodPut, "/api/aura/reader/settings", `{"keep_pages":0}`, alice); env.St != StatusOK {
		t.Fatalf("zero put st=%d", env.St)
	}
	if _, s := f.readerSettings(t, alice); s == nil || s.KeepPages != 1 {
		t.Fatalf("keep_pages 0 should clamp to 1, got %#v", s)
	}

	// 局部 PUT 只改一个字段，其余保留（否则前端一次只带一个键就会清掉别的设置）。
	if _, env := f.call(http.MethodPut, "/api/aura/reader/settings", `{"gap":7}`, alice); env.St != StatusOK {
		t.Fatalf("partial put st=%d", env.St)
	}
	if _, s := f.readerSettings(t, alice); s == nil || s.Gap != 7 || s.Width != 20 || s.KeepPages != 1 {
		t.Fatalf("partial put wiped fields: %#v", s)
	}

	// 完全无法识别的载荷：不报错，回当前值，绝不把已有记录清空。
	if _, env := f.call(http.MethodPut, "/api/aura/reader/settings", `{"nonsense":1}`, alice); env.St != StatusOK {
		t.Fatalf("unknown-key put st=%d", env.St)
	}
	if _, s := f.readerSettings(t, alice); s == nil || s.Gap != 7 {
		t.Fatalf("unknown-key put should not clear record: %#v", s)
	}

	// 用户隔离：bob 不受影响。
	if _, s := f.readerSettings(t, bob); s != nil {
		t.Fatalf("bob should have no settings, got %#v", s)
	}
}
