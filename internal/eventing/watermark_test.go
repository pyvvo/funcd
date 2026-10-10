package eventing

import "testing"

func TestWatermarkContract(t *testing.T) {
	WatermarkContract(t, func(*testing.T) Watermark { return NewMemWatermark() })
}
