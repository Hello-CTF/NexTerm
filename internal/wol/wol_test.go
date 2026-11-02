package wol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestMagicPacketLayout(t *testing.T) {
	mac, err := ParseMAC("aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := MagicPacket(mac)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) != 102 || !bytes.Equal(packet[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Fatalf("bad header: %x", packet)
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(packet[6+i*6:12+i*6], mac) {
			t.Fatalf("repetition %d wrong: %x", i, packet)
		}
	}
}

func TestParseMACIsStrict(t *testing.T) {
	for _, ok := range []string{"AA:BB:CC:DD:EE:01", "aa-bb-cc-dd-ee-01", " 00:11:22:33:44:55 "} {
		if _, err := ParseMAC(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "aabbccddee01", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:01:02", "aa:bb-cc:dd:ee:01", "aa:bb:cc:dd:ee:0g",
		"a:b:c:d:e:f", "aabb.ccdd.ee01", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "aa:bb:cc:dd:ee:0\n", "aa:bb:cc:dd:ee:+1", "0x:bb:cc:dd:ee:01"} {
		if _, err := ParseMAC(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOptionsDestination(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		fail bool
	}{
		{raw: `{"wakeMac":"aa:bb:cc:dd:ee:01"}`, want: "255.255.255.255:9"},
		{raw: `{"wakeMac":"aa:bb:cc:dd:ee:01","wakeBroadcast":"192.168.1.255","wakePort":7}`, want: "192.168.1.255:7"},
		{raw: ``, want: "255.255.255.255:9"},
		{raw: `{"wakeMac":"nope"}`, fail: true},
		{raw: `{"wakeBroadcast":"host.example"}`, fail: true},
		{raw: `{"wakeBroadcast":"::1"}`, fail: true},
		{raw: `{"wakeBroadcast":"127.0.0.1"}`, fail: true},
		{raw: `{"wakeBroadcast":"169.254.169.254"}`, fail: true},
		{raw: `{"wakeBroadcast":"224.0.0.1"}`, fail: true},
		{raw: `{"wakeBroadcast":"0.0.0.0"}`, fail: true},
		{raw: `{"wakeBroadcast":"100.64.0.1"}`, fail: true},
		{raw: `{"wakeBroadcast":"240.0.0.1"}`, fail: true},
		{raw: `{"wakeBroadcast":"08.8.8.8"}`, fail: true},
		{raw: `{"wakeBroadcast":"8.8.8.8"}`, want: "8.8.8.8:9"},
		{raw: `{"wakePort":0}`, fail: true},
		{raw: `{"wakePort":70000}`, fail: true},
		{raw: `{"wakePort":"9"}`, fail: true},
		{raw: `{"wakeMac":5}`, fail: true},
	}
	for _, c := range cases {
		options, err := ParseOptions(c.raw)
		if c.fail {
			if err == nil {
				t.Errorf("%s accepted", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		destination, err := options.Destination()
		if err != nil || destination.String() != c.want {
			t.Errorf("%s: destination %v err %v, want %s", c.raw, destination, err, c.want)
		}
	}
}

func TestSendUDPReachesLocalListener(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("udp loopback unavailable: %v", err)
	}
	defer func() { _ = listener.Close() }()
	mac, _ := ParseMAC("aa:bb:cc:dd:ee:01")
	packet, _ := MagicPacket(mac)
	received := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 512)
		n, _, err := listener.ReadFrom(buffer)
		if err == nil {
			received <- append([]byte(nil), buffer[:n]...)
		}
	}()
	if err := SendUDP(t.Context(), listener.LocalAddr().String(), packet); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if !bytes.Equal(got, packet) {
			t.Fatalf("datagram differs: %x", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("datagram not received")
	}
}

type fakeResolver struct{ assets map[string]Asset }

func (f fakeResolver) ResolveAsset(_ context.Context, id string) (Asset, error) {
	asset, ok := f.assets[id]
	if !ok {
		return Asset{}, ipc.NewError(ipc.CodeNotFound, "资产不存在")
	}
	return asset, nil
}

type recordingAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (r *recordingAuditor) Audit(_ context.Context, event AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

type sentPacket struct {
	target string
	packet []byte
}

type wakeFixture struct {
	svc     *Service
	auditor *recordingAuditor
	local   []sentPacket
	clock   time.Time
	device  func(context.Context, string) (DialFunc, error)
}

func newWakeFixture(t *testing.T, assets ...Asset) *wakeFixture {
	t.Helper()
	f := &wakeFixture{auditor: &recordingAuditor{}, clock: time.Unix(1000, 0)}
	byID := map[string]Asset{}
	for _, a := range assets {
		byID[a.ID] = a
	}
	f.svc = NewService(fakeResolver{byID},
		WithAuditor(f.auditor),
		WithClock(func() time.Time { return f.clock }),
		WithLocalSender(func(_ context.Context, target string, packet []byte) error {
			f.local = append(f.local, sentPacket{target, packet})
			return nil
		}),
		WithDeviceDialer(func(ctx context.Context, via string) (DialFunc, error) {
			if f.device == nil {
				return nil, errors.New("no device")
			}
			return f.device(ctx, via)
		}),
	)
	return f
}

func TestWakeSendsStoredDestinationAndAudits(t *testing.T) {
	f := newWakeFixture(t, Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA-BB-CC-DD-EE-01","wakeBroadcast":"10.0.0.255","wakePort":7}`})
	result, err := f.svc.Wake(ipc.WithUserID(t.Context(), "u1"), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != RouteLocal || result.Target != "10.0.0.255:7" || result.Mac != "AA:BB:CC:DD:EE:01" || result.SentAtMs == 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(f.local) != 1 || f.local[0].target != "10.0.0.255:7" || len(f.local[0].packet) != 102 {
		t.Fatalf("sent = %+v", f.local)
	}
	events := f.auditor.events
	if len(events) != 2 || events[0].Payload["phase"] != "intent" || events[0].Payload["sent"] != false ||
		events[1].Kind != AuditKind || events[1].AssetID != "a1" || events[1].Payload["phase"] != "result" || events[1].Payload["sent"] != true {
		t.Fatalf("audit = %+v", f.auditor.events)
	}
}

func TestWakeRejectsForeignOwnerAndUnsetMAC(t *testing.T) {
	f := newWakeFixture(t,
		Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`},
		Asset{ID: "a2", OwnerID: "u1", Kind: "rdp", OptionsJSON: `{}`},
		Asset{ID: "a3", OwnerID: "u1", Kind: "mysql", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`})
	var coded *ipc.Error
	_, err := f.svc.Wake(ipc.WithUserID(t.Context(), "u2"), "a1")
	if !errors.As(err, &coded) || coded.Code != ipc.CodeForbidden {
		t.Fatalf("foreign owner err = %v", err)
	}
	for _, id := range []string{"a2", "a3", "missing", ""} {
		if _, err := f.svc.Wake(ipc.WithUserID(t.Context(), "u1"), id); err == nil {
			t.Fatalf("%q was accepted", id)
		}
	}
	if len(f.local) != 0 || len(f.auditor.events) != 0 {
		t.Fatalf("rejected requests sent or audited: %+v %+v", f.local, f.auditor.events)
	}
}

func TestWakeChecksAccountSession(t *testing.T) {
	f := newWakeFixture(t, Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`})
	ctx := ipc.WithAccountSessionCheck(ipc.WithUserID(t.Context(), "u1"), func(context.Context) error {
		return ipc.NewError(ipc.CodeForbidden, "登录会话已失效")
	})
	if _, err := f.svc.Wake(ctx, "a1"); err == nil || len(f.local) != 0 {
		t.Fatalf("revoked session woke a host: %v", err)
	}
}

func TestWakeCooldown(t *testing.T) {
	f := newWakeFixture(t, Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`})
	ctx := ipc.WithUserID(t.Context(), "u1")
	if _, err := f.svc.Wake(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Wake(ctx, "a1"); err == nil {
		t.Fatal("second immediate wake accepted")
	}
	f.clock = f.clock.Add(cooldown)
	if _, err := f.svc.Wake(ctx, "a1"); err != nil {
		t.Fatalf("wake after cooldown: %v", err)
	}
}

func wakeAssetWith(broadcast string) Asset {
	return Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01","wakeBroadcast":"` + broadcast + `"}`}
}

func TestServerModeRestrictsTargetsByRole(t *testing.T) {
	user := func(t *testing.T) context.Context { return ipc.WithRole(ipc.WithUserID(t.Context(), "u1"), "user") }
	f := newWakeFixture(t, wakeAssetWith("8.8.8.8"))
	var coded *ipc.Error
	_, err := f.svc.Wake(user(t), "a1")
	if !errors.As(err, &coded) || coded.Code != ipc.CodeForbidden || len(f.local) != 0 {
		t.Fatalf("public target for a normal user: %v sent=%d", err, len(f.local))
	}
	if len(f.auditor.events) != 1 || f.auditor.events[0].Payload["phase"] != "refused" {
		t.Fatalf("refusal not audited: %+v", f.auditor.events)
	}

	admin := ipc.WithRole(ipc.WithUserID(t.Context(), "u1"), "superadmin")
	if _, err := f.svc.Wake(admin, "a1"); err != nil {
		t.Fatalf("superadmin refused: %v", err)
	}

	for _, ok := range []string{"255.255.255.255", "10.1.1.255", "172.16.1.255", "192.168.1.255"} {
		f := newWakeFixture(t, wakeAssetWith(ok))
		if _, err := f.svc.Wake(user(t), "a1"); err != nil {
			t.Errorf("%s refused for a normal user: %v", ok, err)
		}
	}

	// Desktop mode has no owner: any address that is not always denied is allowed.
	f = newWakeFixture(t, Asset{ID: "a1", Kind: "ssh", OptionsJSON: wakeAssetWith("8.8.8.8").OptionsJSON})
	if _, err := f.svc.Wake(t.Context(), "a1"); err != nil {
		t.Fatalf("desktop mode refused a public address: %v", err)
	}
}

func TestAlwaysDeniedTargetsRefusedEvenForSuperadmin(t *testing.T) {
	// Stored options are validated on write, but a row written by sync may still hold one.
	f := newWakeFixture(t, wakeAssetWith("100.64.0.9"))
	admin := ipc.WithRole(ipc.WithUserID(t.Context(), "u1"), "superadmin")
	if _, err := f.svc.Wake(admin, "a1"); err == nil || len(f.local) != 0 {
		t.Fatalf("always-deny target was sent: %v", err)
	}
}

func TestOwnerTokenBucketSpansAssetsAndRefills(t *testing.T) {
	assets := make([]Asset, 0, 12)
	for i := 0; i < 12; i++ {
		assets = append(assets, Asset{ID: fmt.Sprintf("a%d", i), OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`})
	}
	f := newWakeFixture(t, assets...)
	ctx := ipc.WithUserID(t.Context(), "u1")
	sent := 0
	for _, a := range assets {
		if _, err := f.svc.Wake(ctx, a.ID); err == nil {
			sent++
		}
	}
	if sent != 10 {
		t.Fatalf("sent %d of 12 distinct assets in one instant; want the burst of 10", sent)
	}
	other := ipc.WithUserID(t.Context(), "u2")
	f.svc.resolver = fakeResolver{map[string]Asset{"b": {ID: "b", OwnerID: "u2", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`}}}
	if _, err := f.svc.Wake(other, "b"); err != nil {
		t.Fatalf("another owner shares the bucket: %v", err)
	}
	f.svc.resolver = fakeResolver{map[string]Asset{"a11": assets[11]}}
	f.clock = f.clock.Add(bucketInterval)
	if _, err := f.svc.Wake(ctx, "a11"); err != nil {
		t.Fatalf("bucket did not refill: %v", err)
	}
}

type failingAuditor struct {
	recordingAuditor
	failPhase string
}

func (a *failingAuditor) Audit(ctx context.Context, event AuditEvent) error {
	if event.Payload["phase"] == a.failPhase {
		return errors.New("disk full")
	}
	return a.recordingAuditor.Audit(ctx, event)
}

func TestAuditIntentFailureBlocksTheSendAndResultFailureIsVisible(t *testing.T) {
	asset := Asset{ID: "a1", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01"}`}
	f := newWakeFixture(t, asset)
	f.svc.auditor = &failingAuditor{failPhase: "intent"}
	ctx := ipc.WithUserID(t.Context(), "u1")
	if _, err := f.svc.Wake(ctx, "a1"); err == nil || len(f.local) != 0 {
		t.Fatalf("packet sent without an intent record: %v", err)
	}

	f = newWakeFixture(t, asset)
	f.svc.auditor = &failingAuditor{failPhase: "result"}
	result, err := f.svc.Wake(ctx, "a1")
	if err != nil || len(f.local) != 1 {
		t.Fatalf("result-audit failure must not undo the send: %v", err)
	}
	if !strings.Contains(result.Note, "审计") {
		t.Fatalf("result audit failure not visible: %q", result.Note)
	}
}

func TestDeviceRefusalIsNotReportedAsOldAgent(t *testing.T) {
	f := newWakeFixture(t, Asset{ID: "dev", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01","viaAssetId":"device/d1"}`})
	f.device = func(context.Context, string) (DialFunc, error) {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("隧道拨号失败: %w", deviceproto.CodedError(deviceproto.CodeInvalid, "device request rejected: udp target refused (不能是回环地址)"))
		}, nil
	}
	_, err := f.svc.Wake(ipc.WithUserID(t.Context(), "u1"), "dev")
	var coded *ipc.Error
	if !errors.As(err, &coded) || coded.Code != ipc.CodeForbidden || strings.Contains(coded.Message, "版本过旧") || !strings.Contains(coded.Message, "udp target refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestWakeRoutesThroughDeviceOnly(t *testing.T) {
	f := newWakeFixture(t,
		Asset{ID: "dev", OwnerID: "u1", Kind: "rdp", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01","viaAssetId":"device/d1","wakeBroadcast":"192.168.1.255"}`},
		Asset{ID: "ssh", OwnerID: "u1", Kind: "vnc", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01","viaAssetId":"ssh-asset"}`})
	ctx := ipc.WithUserID(t.Context(), "u1")

	var gotVia, gotNetwork, gotAddr string
	wroteCh := make(chan []byte, 1)
	f.device = func(_ context.Context, via string) (DialFunc, error) {
		gotVia = via
		return func(_ context.Context, network, address string) (net.Conn, error) {
			gotNetwork, gotAddr = network, address
			client, agent := net.Pipe()
			go func() {
				buf := make([]byte, 512)
				n, _ := agent.Read(buf)
				wroteCh <- append([]byte(nil), buf[:n]...)
				_ = agent.Close()
			}()
			return client, nil
		}, nil
	}
	result, err := f.svc.Wake(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	wrote := <-wroteCh
	if result.Route != RouteDevice || gotVia != "device/d1" || gotNetwork != "udp4" || gotAddr != "192.168.1.255:9" || len(wrote) != 102 || len(f.local) != 0 {
		t.Fatalf("device route: result=%+v via=%s net=%s addr=%s wrote=%d local=%d", result, gotVia, gotNetwork, gotAddr, len(wrote), len(f.local))
	}

	result, err = f.svc.Wake(ctx, "ssh")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != RouteLocal || result.Note == "" || len(f.local) != 1 {
		t.Fatalf("ssh via should send locally with a visible note: %+v", result)
	}
}

func TestWakeDeviceFailuresAreVisibleAndAudited(t *testing.T) {
	f := newWakeFixture(t, Asset{ID: "dev", OwnerID: "u1", Kind: "ssh", OptionsJSON: `{"wakeMac":"AA:BB:CC:DD:EE:01","viaAssetId":"device/d1"}`})
	ctx := ipc.WithUserID(t.Context(), "u1")

	f.device = func(context.Context, string) (DialFunc, error) {
		return nil, ipc.NewError(ipc.CodeDisconnected, "设备没有已连接会话")
	}
	if _, err := f.svc.Wake(ctx, "dev"); err == nil || len(f.local) != 0 {
		t.Fatalf("offline device must fail without a local fallback: %v", err)
	}

	f.clock = f.clock.Add(time.Minute)
	f.device = func(context.Context, string) (DialFunc, error) {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("隧道拨号失败: %w", deviceproto.CodedError(deviceproto.CodeInvalid, "unsupported dial network"))
		}, nil
	}
	var coded *ipc.Error
	_, err := f.svc.Wake(ctx, "dev")
	if !errors.As(err, &coded) || coded.Code != ipc.CodeUnsupported {
		t.Fatalf("old agent err = %v", err)
	}
	if len(f.auditor.events) != 4 || f.auditor.events[3].Payload["sent"] != false || f.auditor.events[3].Payload["phase"] != "result" {
		t.Fatalf("failures not audited: %+v", f.auditor.events)
	}
}
