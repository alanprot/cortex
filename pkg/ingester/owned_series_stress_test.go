package ingester

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/stretchr/testify/require"

	"github.com/cortexproject/cortex/pkg/ring"
)

// Heavy runs are opt-in: OWNED_SERIES_STRESS=1.
const ownedStressEnv = "OWNED_SERIES_STRESS"

func ownedStressHeavy() bool { return os.Getenv(ownedStressEnv) != "" }

func skipUnlessOwnedStress(t testing.TB) {
	if !ownedStressHeavy() {
		t.Skip("set " + ownedStressEnv + "=1")
	}
}

// inRangesBrute checks ranges directly, independent of toIntervals.
func inRangesBrute(tok uint32, ranges []ring.TokenRange) bool {
	for _, r := range ranges {
		if r.Start < r.End {
			if tok >= r.Start && tok < r.End {
				return true
			}
			continue
		}
		if tok >= r.Start || tok < r.End {
			return true
		}
	}
	return false
}

// stressRangeSet returns empty, whole-ring, or up to 1500 random ranges, some wrapping.
func stressRangeSet(r *rand.Rand) []ring.TokenRange {
	switch r.Intn(10) {
	case 0:
		return []ring.TokenRange{}
	case 1:
		s := r.Uint32()
		return []ring.TokenRange{{Start: s, End: s}}
	}
	out := make([]ring.TokenRange, 1+r.Intn(1500))
	for i := range out {
		s := r.Uint32()
		out[i] = ring.TokenRange{Start: s, End: s + uint32(1+r.Intn(1<<22))}
	}
	return out
}

func stressToken(id uint64, space uint32) uint32 {
	z := id + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	tok := uint32(z ^ (z >> 31))
	if space > 0 {
		tok %= space
	}
	return tok
}

// trackerState is only exact once hooks and recounts have stopped.
func trackerState(tr *ownedSeriesTracker) (total, notOwned, card int64, extra int64) {
	total, notOwned = tr.totals()
	_, _, card, extra = tr.bitmapStats()
	return total, notOwned, card, extra
}

// TestOwnedSeriesTracker_Stress runs concurrent creates and deletes against
// back-to-back recounts, then checks every count against brute force.
func TestOwnedSeriesTracker_Stress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		space uint32
	}{
		{"random-tokens", 0},
		{"heavy-collisions", 1 << 12},
	} {
		t.Run(tc.name, func(t *testing.T) { runOwnedTrackerStress(t, tc.space) })
	}
}

func runOwnedTrackerStress(t *testing.T, space uint32) {
	const workers = 16
	perWorker, rounds, phase := 20_000, 5, 150*time.Millisecond
	if ownedStressHeavy() {
		perWorker, rounds, phase = 250_000, 20, 500*time.Millisecond
	}
	tr := newOwnedSeriesTracker()
	live := make([][]bool, workers)
	for w := range live {
		live[w] = make([]bool, perWorker)
	}
	tok := func(w, i int) uint32 { return stressToken(uint64(w*perWorker+i), space) }
	rr := rand.New(rand.NewSource(ownershipTestSeed))
	var hookOps, recounts, violations atomic.Int64

	for round := range rounds {
		var stopWorkers, stopRecount atomic.Bool
		var wwg, rwg sync.WaitGroup
		for w := range workers {
			wwg.Add(1)
			go func(w int) {
				defer wwg.Done()
				r := rand.New(rand.NewSource(int64(round*workers + w)))
				for !stopWorkers.Load() {
					i := r.Intn(perWorker)
					if live[w][i] {
						tr.remove(tok(w, i))
					} else {
						tr.add(tok(w, i))
					}
					live[w][i] = !live[w][i]
					hookOps.Add(1)
				}
			}(w)
		}
		rwg.Add(2)
		go func() {
			defer rwg.Done()
			r := rand.New(rand.NewSource(int64(1000 + round)))
			for !stopRecount.Load() {
				if r.Intn(20) == 0 {
					tr.clearRanges()
				} else {
					tr.setRanges(stressRangeSet(r))
				}
				recounts.Add(1)
			}
		}()
		go func() {
			defer rwg.Done()
			for !stopRecount.Load() {
				total, notOwned, _, _ := trackerState(tr)
				if notOwned < 0 || notOwned > total {
					violations.Add(1)
				}
			}
		}()

		time.Sleep(phase)
		stopRecount.Store(true)
		rwg.Wait()

		// Hooks only, against a known final range set.
		final := stressRangeSet(rr)
		tr.setRanges(final)
		time.Sleep(phase / 2)
		stopWorkers.Store(true)
		wwg.Wait()

		var wantTotal, wantNotOwned int64
		for w := range live {
			for i, l := range live[w] {
				if l {
					wantTotal++
					if !inRangesBrute(tok(w, i), final) {
						wantNotOwned++
					}
				}
			}
		}
		total, notOwned, card, extra := trackerState(tr)
		require.Equal(t, wantTotal, total, "round %d total", round)
		require.Equal(t, card+extra, total, "round %d bitmap+extra", round)
		require.Equal(t, wantNotOwned, notOwned, "round %d incremental notOwned", round)
		require.Equal(t, notOwned, tr.notOwned.Load(), "round %d atomic sum vs stripes", round)

		// A recount from scratch must agree with the incremental count.
		tr.setRanges([]ring.TokenRange{{Start: 7, End: 7}})
		tr.setRanges(final)
		_, notOwned, _, _ = trackerState(tr)
		require.Equal(t, wantNotOwned, notOwned, "round %d recount notOwned", round)
	}
	require.Zero(t, violations.Load(), "notOwned outside [0,total]")
	t.Logf("hook ops %d, recounts %d, rounds %d", hookOps.Load(), recounts.Load(), rounds)
}

// TestOwnedSeriesTracker_CollisionCounterOverflow puts more series on one
// token than uint16 holds.
func TestOwnedSeriesTracker_CollisionCounterOverflow(t *testing.T) {
	const n = 70_000
	tr := newOwnedSeriesTracker()
	for range n {
		tr.add(42)
	}
	for range n - 1 {
		tr.remove(42)
	}
	total, _, card, extra := trackerState(tr)
	require.EqualValues(t, 1, total)
	require.EqualValues(t, 1, card+extra, "bitmap+extra must still hold the last series")
}

func stressPercentiles(d []time.Duration) string {
	if len(d) == 0 {
		return "n/a"
	}
	slices.Sort(d)
	p := func(q float64) time.Duration { return d[min(len(d)-1, int(q*float64(len(d))))] }
	return fmt.Sprintf("p50=%v p99=%v p99.9=%v p99.99=%v max=%v", p(0.5), p(0.99), p(0.999), p(0.9999), d[len(d)-1])
}

func prefilledTracker(n int, space uint32) (*ownedSeriesTracker, []uint32) {
	tr := newOwnedSeriesTracker()
	tokens := make([]uint32, n)
	for i := range tokens {
		tokens[i] = stressToken(uint64(i), space)
		tr.add(tokens[i])
	}
	return tr, tokens
}

// TestOwnedSeriesStress_HookThroughput measures create+delete throughput by goroutine count.
func TestOwnedSeriesStress_HookThroughput(t *testing.T) {
	skipUnlessOwnedStress(t)
	const dur = 2 * time.Second
	for _, n := range []int{1_000_000, 10_000_000} {
		tr, tokens := prefilledTracker(n, 0)
		tr.setRanges(randomOwnedRanges(rand.New(rand.NewSource(1)), pushBenchOwnedRanges, pushBenchZoneSize))
		for _, g := range []int{1, 8, 48} {
			var ops atomic.Int64
			var stop atomic.Bool
			var wg sync.WaitGroup
			for w := range g {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					// Each goroutine churns its own slice of series.
					lo, hi := w*n/g, (w+1)*n/g
					for k := lo; !stop.Load(); k++ {
						if k == hi {
							k = lo
						}
						tr.remove(tokens[k])
						tr.add(tokens[k])
						ops.Add(2)
					}
				}(w)
			}
			time.Sleep(dur)
			stop.Store(true)
			wg.Wait()
			mops := float64(ops.Load()) / dur.Seconds() / 1e6
			t.Logf("series=%d goroutines=%2d  %.2f M hooks/s  (%.0f ns/hook wall)", n, g, mops, 1e3/mops)
		}
	}
}

// TestOwnedSeriesStress_HookLatency measures hook latency with and without
// back-to-back recounts, which hold the tracker lock.
func TestOwnedSeriesStress_HookLatency(t *testing.T) {
	skipUnlessOwnedStress(t)
	const (
		goroutines = 8
		dur        = 3 * time.Second
	)
	for _, n := range []int{1_000_000, 10_000_000} {
		tr, tokens := prefilledTracker(n, 0)
		r := rand.New(rand.NewSource(2))
		sets := [][]ring.TokenRange{
			randomOwnedRanges(r, pushBenchOwnedRanges, pushBenchZoneSize),
			randomOwnedRanges(r, pushBenchOwnedRanges, pushBenchZoneSize),
		}
		tr.setRanges(sets[0])
		for _, recount := range []bool{false, true} {
			var stop atomic.Bool
			var wg sync.WaitGroup
			lat := make([][]time.Duration, goroutines)
			var recountDur []time.Duration
			if recount {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for k := 0; !stop.Load(); k++ {
						s := time.Now()
						tr.setRanges(sets[k%2])
						recountDur = append(recountDur, time.Since(s))
					}
				}()
			}
			for w := range goroutines {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					lo, hi := w*n/goroutines, (w+1)*n/goroutines
					l := make([]time.Duration, 0, 1<<22)
					for k := lo; !stop.Load(); k++ {
						if k == hi {
							k = lo
						}
						s := time.Now()
						tr.remove(tokens[k])
						tr.add(tokens[k])
						l = append(l, time.Since(s))
					}
					lat[w] = l
				}(w)
			}
			time.Sleep(dur)
			stop.Store(true)
			wg.Wait()
			all := slices.Concat(lat...)
			t.Logf("series=%d recount=%v  ops=%d  remove+add latency: %s", n, recount, len(all), stressPercentiles(all))
			if recount {
				t.Logf("series=%d  recounts=%d  recount duration: %s", n, len(recountDur), stressPercentiles(recountDur))
			}
		}
	}
}

// TestOwnedSeriesStress_RecountCost measures one recount by series count,
// range count and collision level, split into mask build and the rest.
func TestOwnedSeriesStress_RecountCost(t *testing.T) {
	skipUnlessOwnedStress(t)
	const reps = 20
	for _, n := range []int{1_000_000, 10_000_000} {
		for _, space := range []uint32{0, 1 << 20} {
			tr, _ := prefilledTracker(n, space)
			_, _, card, extra := trackerState(tr)
			for _, nr := range []int{128, 512, 1536, 4096} {
				r := rand.New(rand.NewSource(int64(nr)))
				sets := [][]ring.TokenRange{
					randomOwnedRanges(r, nr, pushBenchZoneSize),
					randomOwnedRanges(r, nr, pushBenchZoneSize),
				}
				var total, mask []time.Duration
				for k := range reps {
					s := time.Now()
					tr.setRanges(sets[k%2])
					total = append(total, time.Since(s))

					s = time.Now()
					m := roaring.New()
					for _, iv := range toIntervals(sets[k%2]) {
						m.AddRange(iv.start, iv.end)
					}
					mask = append(mask, time.Since(s))
				}
				t.Logf("series=%-9d tokens=%-9d extraSeries=%-8d ranges=%-5d  recount %s | mask build %s",
					n, card, extra, nr, stressPercentiles(total), stressPercentiles(mask))
			}
		}
	}
}
