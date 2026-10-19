package main

import "testing"

// The first step is logged with step 0 and the second step, an /offer request
// that carries the landing's click id, with step 1.
func TestEventStepMarksOfferStep(t *testing.T) {
	for _, method := range []string{"ip", "cookie"} {
		t.Run(method, func(t *testing.T) {
			var opts []func(*engineDeps)
			if method == "ip" {
				opts = append(opts, withRedis(t))
			}
			h, ins, stop := curlEnv(t, routedGroup(method), "0", opts...)
			j := jar{}
			offerPath := "/g1/offer?cid=" + signedCID()
			for _, path := range []string{"/g1", offerPath, offerPath} {
				j.visit(t, h, path)
			}
			stop()
			want := []uint8{0, 1, 1}
			if len(ins.events) != len(want) {
				t.Fatalf("events %d, want %d", len(ins.events), len(want))
			}
			for i, w := range want {
				if got := ins.events[i].Step; got != w {
					t.Errorf("event %d: Step=%d, want %d", i, got, w)
				}
			}
		})
	}
}

// A visit to /offer without a click id this engine issued, or with a foreign
// one, is a first step: the step names the click's place in the funnel, not the
// path of the request.
func TestEventStepDirectOffer(t *testing.T) {
	for _, path := range []string{"/g1/offer", "/g1/offer?cid=a1b2c3d4e5f6", "/g1/offer?cid="} {
		h, ins, stop := curlEnv(t, routedGroup("ip"), "0", withRedis(t))
		jar{}.visit(t, h, path)
		stop()
		if len(ins.events) != 1 || ins.events[0].Step != 0 {
			t.Errorf("%s: events %+v", path, ins.events)
		}
	}
}
