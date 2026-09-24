package behavior

import "math"

// Aggregate averages the valid feature values by weight, then applies
// the convex curve (gamma>1 keeps low suspicion cheap). Weight-0
// features are excluded. With no valid feature the IP-reputation
// fallback decides (0 for a clean IP, 1 for a blocklisted one).
func Aggregate(vals map[FeatureID]float64, params map[FeatureID]FeatureParam, gamma float64, fallback float64) float64 {
	var sum, wsum float64
	for id, v := range vals {
		fp, ok := params[id]
		if !ok || fp.Weight <= 0 {
			continue
		}
		sum += fp.Weight * v
		wsum += fp.Weight
	}
	if wsum <= 0 {
		return clamp01(fallback)
	}
	return math.Pow(clamp01(sum/wsum), gamma)
}

// NextTier moves the hysteresis state toward the score: it steps up
// across every entered threshold and down across every exited one.
// enter/exit index tiers 1..maxTier (slot 0 unused); short slices
// simply stop the walk instead of panicking.
func NextTier(s float64, cur int, enter, exit []float64, maxTier int) int {
	if cur < 0 {
		cur = 0
	}
	if cur > maxTier {
		cur = maxTier
	}
	for cur < maxTier && cur+1 < len(enter) && s >= enter[cur+1] {
		cur++
	}
	for cur > 0 && cur < len(exit) && s < exit[cur] {
		cur--
	}
	return cur
}

// GroupAggregate folds member suspicions into one group score:
// beta scales the mix of worst member and group mean, so one bad
// actor taints the group without a lone outlier condemning it.
func GroupAggregate(scores []float64, beta float64) float64 {
	if len(scores) == 0 || beta <= 0 {
		return 0
	}
	mx := scores[0]
	sum := 0.0
	for _, s := range scores {
		if s > mx {
			mx = s
		}
		sum += s
	}
	return clamp01(beta * (0.6*mx + 0.4*sum/float64(len(scores))))
}

// Combine takes the worse of own and group suspicion.
func Combine(own, group float64) float64 {
	if group > own {
		return group
	}
	return own
}

// AttackScale folds the four attack signals into [0,1]. Mode
// "weighted" averages by w, anything else takes the max
// (conservative: an unknown mode must not mute an attack).
func AttackScale(reject, conn, ip, bl float64, mode string, w [4]float64) float64 {
	rs := [4]float64{clamp01(reject), clamp01(conn), clamp01(ip), clamp01(bl)}
	if mode == "weighted" {
		var sum, wsum float64
		for i, r := range rs {
			if w[i] > 0 {
				sum += w[i] * r
				wsum += w[i]
			}
		}
		if wsum <= 0 {
			return 0
		}
		return clamp01(sum / wsum)
	}
	mx := rs[0]
	for _, r := range rs[1:] {
		if r > mx {
			mx = r
		}
	}
	return mx
}

// ApplyBump adds the attack bump (rounded scale share of maxBump) to
// the behavior tier, clamped at tierMax.
func ApplyBump(base int, scale float64, maxBump, tierMax int) int {
	bump := int(math.Round(clamp01(scale) * float64(maxBump)))
	out := base + bump
	if out > tierMax {
		return tierMax
	}
	if out < 0 {
		return 0
	}
	return out
}
