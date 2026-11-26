package leg

import (
	"context"
	"encoding/binary"
	"sync"
	"time"
)

// LossWindow is the window Stats.Loss covers (design 4.4).
const LossWindow = 10 * time.Second

const sampleInterval = time.Second

// Candidate describes one end of the selected ICE pair.
type Candidate struct {
	Type    string // host, srflx, prflx
	Network string // udp4, udp6
	Address string
	Port    int
}

// Stats is a snapshot of the leg.
type Stats struct {
	// RTT is QUIC's smoothed RTT; LatestRTT and MinRTT are its other
	// estimates. All are zero before the first sample.
	RTT, LatestRTT, MinRTT time.Duration
	// Loss is the fraction of QUIC packets declared lost over LossWindow
	// (or since the leg started, if younger).
	Loss float64
	// PacketsSent and PacketsLost are the totals Loss is computed from.
	PacketsSent, PacketsLost uint64
	// BytesSent and BytesReceived count QUIC payload bytes, retransmissions
	// included.
	BytesSent, BytesReceived uint64
	// Dropped counts packets the ICE adapter discarded (write failures,
	// oversized reads).
	Dropped uint64
	// Local and Remote are the selected candidate pair.
	Local, Remote Candidate
	// Since is when the leg was established.
	Since time.Time
}

type lossSample struct {
	at         time.Time
	sent, lost uint64
}

// lossSampler keeps one sample per second for the loss window.
type lossSampler struct {
	mu      sync.Mutex
	samples []lossSample
}

func (s *lossSampler) add(x lossSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, x)
	cut := 0
	for cut < len(s.samples)-1 && x.at.Sub(s.samples[cut+1].at) >= LossWindow {
		cut++
	}
	s.samples = s.samples[cut:]
}

func (s *lossSampler) loss(now lossSample) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var base lossSample
	for _, x := range s.samples {
		if now.at.Sub(x.at) <= LossWindow {
			base = x
			break
		}
	}
	if now.sent <= base.sent || now.lost <= base.lost {
		return 0
	}
	return float64(now.lost-base.lost) / float64(now.sent-base.sent)
}

func (l *leg) sampleLoop() {
	defer l.wg.Done()
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	for {
		st := l.conn.ConnectionStats()
		l.sampler.add(lossSample{at: time.Now(), sent: st.PacketsSent, lost: st.PacketsLost})
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (l *leg) Stats() Stats {
	st := l.conn.ConnectionStats()
	now := lossSample{at: time.Now(), sent: st.PacketsSent, lost: st.PacketsLost}
	out := Stats{
		RTT:           st.SmoothedRTT,
		LatestRTT:     st.LatestRTT,
		MinRTT:        st.MinRTT,
		Loss:          l.sampler.loss(now),
		PacketsSent:   st.PacketsSent,
		PacketsLost:   st.PacketsLost,
		BytesSent:     st.BytesSent,
		BytesReceived: st.BytesReceived,
		Dropped:       l.pconn.dropped.Load(),
		Since:         l.since,
	}
	if pair, err := l.agent.GetSelectedCandidatePair(); err == nil && pair != nil {
		out.Local = Candidate{Type: pair.Local.Type().String(), Network: pair.Local.NetworkType().String(), Address: pair.Local.Address(), Port: pair.Local.Port()}
		out.Remote = Candidate{Type: pair.Remote.Type().String(), Network: pair.Remote.NetworkType().String(), Address: pair.Remote.Address(), Port: pair.Remote.Port()}
	}
	return out
}

const (
	dgramPing byte = 1
	dgramPong byte = 2
)

type pinger struct {
	mu      sync.Mutex
	next    uint64
	waiters map[uint64]chan struct{}
}

func (p *pinger) init() { p.waiters = make(map[uint64]chan struct{}) }

func (p *pinger) register() (uint64, chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	ch := make(chan struct{})
	p.waiters[p.next] = ch
	return p.next, ch
}

func (p *pinger) forget(id uint64) {
	p.mu.Lock()
	delete(p.waiters, id)
	p.mu.Unlock()
}

func (p *pinger) resolve(id uint64) {
	p.mu.Lock()
	ch, ok := p.waiters[id]
	delete(p.waiters, id)
	p.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (l *leg) Ping(ctx context.Context) (time.Duration, error) {
	if l.closed() {
		return 0, ErrClosed
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultPingTimeout)
		defer cancel()
	}
	id, ch := l.pings.register()
	defer l.pings.forget(id)
	var msg [9]byte
	msg[0] = dgramPing
	binary.BigEndian.PutUint64(msg[1:], id)
	start := time.Now()
	if err := l.conn.SendDatagram(msg[:]); err != nil {
		return 0, l.streamError(err)
	}
	select {
	case <-ch:
		return time.Since(start), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-l.ctx.Done():
		return 0, ErrClosed
	case <-l.conn.Context().Done():
		return 0, ErrClosed
	}
}

func (l *leg) datagramLoop() {
	defer l.wg.Done()
	ignored := 0
	for {
		b, err := l.conn.ReceiveDatagram(l.ctx)
		if err != nil {
			return
		}
		if len(b) != 9 {
			continue
		}
		switch b[0] {
		case dgramPing:
			if ignored < l.cfg.ignorePings {
				ignored++
				continue
			}
			b[0] = dgramPong
			_ = l.conn.SendDatagram(b)
		case dgramPong:
			l.pings.resolve(binary.BigEndian.Uint64(b[1:]))
		}
	}
}
