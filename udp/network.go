package udp

import "time"

type RTTMonitor struct {
	SmoothedRTT  time.Duration
	LastPingTime time.Time
}

func (r *RTTMonitor) UpdateRTT(latest time.Duration) {
	const alpha = 0.125
	if r.SmoothedRTT == 0 {
		r.SmoothedRTT = latest
	} else {
		r.SmoothedRTT = time.Duration(
			(1-alpha)*float64(r.SmoothedRTT) + alpha*float64(latest),
		)
	}
}

type BandwidthEstimator struct {
	lastTime   time.Time
	bytesAcked int64
	Bandwidth  float64 // bits/sec
}

func (b *BandwidthEstimator) Update(ackedBytes int, updateSize func()) {
	now := time.Now()
	duration := now.Sub(b.lastTime).Seconds()
	if duration > 0.3 {
		b.Bandwidth = float64(b.bytesAcked*8) / duration // bits/sec
		b.bytesAcked = 0
		b.lastTime = now
		updateSize()
	} else {
		b.bytesAcked += int64(ackedBytes)
	}
}
