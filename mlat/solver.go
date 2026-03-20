package mlat

import (
	"fmt"
	"math"
	"sort"
)

const (
	C = 299_702_547.0

	robustSubsetSize           = 4
	inlierResidualMeters       = 3000.0
	maxRMSResidualMeters       = 5000.0
	maxObservedSpanSlackMeters = 5000.0
)

type Vec3 struct{ X, Y, Z float64 }

func (a Vec3) Sub(b Vec3) Vec3      { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Add(b Vec3) Vec3      { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Scale(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Norm() float64        { return math.Sqrt(a.X*a.X + a.Y*a.Y + a.Z*a.Z) }
func (a Vec3) Dist(b Vec3) float64  { return a.Sub(b).Norm() }

func GeodeticToECEF(lat, lon, alt float64) Vec3 {
	const (
		a  = 6378137.0
		e2 = 0.00669437999014
	)
	latR := lat * math.Pi / 180
	lonR := lon * math.Pi / 180
	N := a / math.Sqrt(1-e2*math.Sin(latR)*math.Sin(latR))
	return Vec3{
		X: (N + alt) * math.Cos(latR) * math.Cos(lonR),
		Y: (N + alt) * math.Cos(latR) * math.Sin(lonR),
		Z: (N*(1-e2) + alt) * math.Sin(latR),
	}
}

func ECEFToGeodetic(pos Vec3) (lat, lon, alt float64) {
	const (
		a   = 6378137.0
		b   = 6356752.3142
		e2  = 0.00669437999014
		ep2 = (a*a - b*b) / (b * b)
	)
	p := math.Sqrt(pos.X*pos.X + pos.Y*pos.Y)
	theta := math.Atan2(pos.Z*a, p*b)
	lon = math.Atan2(pos.Y, pos.X) * 180 / math.Pi
	lat = math.Atan2(
		pos.Z+ep2*b*math.Pow(math.Sin(theta), 3),
		p-e2*a*math.Pow(math.Cos(theta), 3),
	) * 180 / math.Pi
	latR := lat * math.Pi / 180
	N := a / math.Sqrt(1-e2*math.Sin(latR)*math.Sin(latR))
	alt = p/math.Cos(latR) - N
	return
}

type fitCandidate struct {
	pos       Vec3
	emission  float64
	sumSq     float64
	rms       float64
	inliers   []int
	residuals []float64
}

func Solve(icao string, obs []Observation) (*MLATResult, error) {
	if len(obs) < MinSensors {
		return nil, fmt.Errorf("need at least %d sensors, got %d", MinSensors, len(obs))
	}

	sort.Slice(obs, func(i, j int) bool {
		return obs[i].TimestampNs() < obs[j].TimestampNs()
	})

	rxPos := make([]Vec3, len(obs))
	arrivalTimes := make([]float64, len(obs))
	for i, o := range obs {
		rxPos[i] = GeodeticToECEF(o.SensorLat, o.SensorLon, o.SensorAlt)
		arrivalTimes[i] = o.CorrectedTimestampSeconds()
	}

	if err := validateObservations(obs, rxPos, arrivalTimes); err != nil {
		return nil, fmt.Errorf("invalid MLAT observations for %s: %w", icao, err)
	}

	subsets := combinationIndices(len(obs), robustSubsetSize)
	if len(subsets) == 0 {
		subsets = [][]int{makeSequentialIndices(len(obs))}
	}

	best, ok := fitRobust(rxPos, arrivalTimes, subsets)
	if !ok {
		return nil, fmt.Errorf("solver failed to converge for %s", icao)
	}

	lat, lon, alt := ECEFToGeodetic(best.pos)
	if alt < -500 || alt > 15000 {
		return nil, fmt.Errorf("unrealistic altitude %.0fm for %s", alt, icao)
	}

	gdop := computeGDOP(best.pos, selectVec3s(rxPos, best.inliers))
	rmsResidualM := best.rms * C
	uncertaintyM := estimateUncertaintyMeters(rmsResidualM, gdop, len(best.inliers))
	qualityScore := computeQualityScore(rmsResidualM, gdop, len(best.inliers))

	return &MLATResult{
		ICAO:              icao,
		Lat:               lat,
		Lon:               lon,
		Alt:               alt,
		Cost:              best.sumSq,
		NumSensors:        len(best.inliers),
		TimestampSec:      best.emission,
		SensorResidualSec: residualMapForInliers(obs, best.inliers, best.residuals),
		RMSResidualM:      rmsResidualM,
		UncertaintyM:      uncertaintyM,
		GDOP:              gdop,
		QualityScore:      qualityScore,
		QualityLabel:      qualityLabel(qualityScore),
		Contributors:      contributorsForInliers(obs, best.inliers, best.residuals),
	}, nil
}

func fitRobust(rxPos []Vec3, arrivalTimes []float64, subsets [][]int) (fitCandidate, bool) {
	best := fitCandidate{}
	found := false

	for _, subset := range subsets {
		subsetPos := selectVec3s(rxPos, subset)
		subsetArrival := selectFloat64s(arrivalTimes, subset)

		pos, emission, _, err := fitObservationSet(subsetPos, subsetArrival)
		if err != nil {
			continue
		}

		candidate, ok := refineCandidate(pos, emission, rxPos, arrivalTimes)
		if !ok {
			continue
		}

		if !found || betterCandidate(candidate, best) {
			best = candidate
			found = true
		}
	}

	if !found {
		return fitCandidate{}, false
	}

	return best, true
}

func refineCandidate(pos Vec3, emission float64, rxPos []Vec3, arrivalTimes []float64) (fitCandidate, bool) {
	residuals := computeTOAResiduals(pos, emission, rxPos, arrivalTimes)
	inliers := pickInliers(residuals, inlierResidualMeters/C)
	if len(inliers) < MinSensors {
		return fitCandidate{}, false
	}

	refinedPos, refinedEmission, _, err := fitObservationSet(selectVec3s(rxPos, inliers), selectFloat64s(arrivalTimes, inliers))
	if err != nil {
		return fitCandidate{}, false
	}

	refinedResiduals := computeTOAResiduals(refinedPos, refinedEmission, rxPos, arrivalTimes)
	refinedInliers := pickInliers(refinedResiduals, inlierResidualMeters/C)
	if len(refinedInliers) < MinSensors {
		return fitCandidate{}, false
	}

	sumSq, rms := residualMetrics(refinedResiduals, refinedInliers)
	if rms > maxRMSResidualMeters/C {
		return fitCandidate{}, false
	}

	return fitCandidate{
		pos:       refinedPos,
		emission:  refinedEmission,
		sumSq:     sumSq,
		rms:       rms,
		inliers:   refinedInliers,
		residuals: refinedResiduals,
	}, true
}

func validateObservations(obs []Observation, rxPos []Vec3, arrivalTimes []float64) error {
	seenSensors := make(map[int64]struct{}, len(obs))
	for _, o := range obs {
		if _, exists := seenSensors[o.SensorID]; exists {
			return fmt.Errorf("duplicate sensor %d", o.SensorID)
		}
		seenSensors[o.SensorID] = struct{}{}
	}

	maxBaseline := 0.0
	for i := 0; i < len(rxPos); i++ {
		for j := i + 1; j < len(rxPos); j++ {
			if d := rxPos[i].Dist(rxPos[j]); d > maxBaseline {
				maxBaseline = d
			}
		}
	}
	if maxBaseline == 0 {
		return fmt.Errorf("sensor geometry is degenerate")
	}

	minArrival, maxArrival := arrivalTimes[0], arrivalTimes[0]
	for _, t := range arrivalTimes[1:] {
		if t < minArrival {
			minArrival = t
		}
		if t > maxArrival {
			maxArrival = t
		}
	}

	maxPhysicalSpan := (maxBaseline + maxObservedSpanSlackMeters) / C
	if maxArrival-minArrival > maxPhysicalSpan {
		return fmt.Errorf("arrival spread %.6fs exceeds physical limit %.6fs", maxArrival-minArrival, maxPhysicalSpan)
	}

	return nil
}

func fitObservationSet(rxPos []Vec3, arrivalTimes []float64) (Vec3, float64, float64, error) {
	var cx, cy, cz float64
	for _, r := range rxPos {
		cx += r.X
		cy += r.Y
		cz += r.Z
	}
	n := float64(len(rxPos))
	centroid := Vec3{cx / n, cy / n, cz / n}
	norm := centroid.Norm()
	if norm == 0 {
		return Vec3{}, 0, 0, fmt.Errorf("invalid centroid")
	}

	startAltitudes := []float64{1000, 3000, 6000, 10000, 12000}
	bestPos := Vec3{}
	bestEmission := 0.0
	bestCost := math.MaxFloat64
	found := false
	for _, altGuess := range startAltitudes {
		startPos := centroid.Scale((norm + altGuess) / norm)
		startEmission := estimateEmissionTime(startPos, rxPos, arrivalTimes)
		pos, emission, cost, err := runLevenbergMarquardtTOA(startPos, startEmission, rxPos, arrivalTimes)
		if err != nil {
			continue
		}
		if cost < bestCost {
			bestPos = pos
			bestEmission = emission
			bestCost = cost
			found = true
		}
	}

	if !found {
		return Vec3{}, 0, 0, fmt.Errorf("no converged start point")
	}
	return bestPos, bestEmission, bestCost, nil
}

func estimateEmissionTime(pos Vec3, rxPos []Vec3, arrivalTimes []float64) float64 {
	total := 0.0
	for i, rx := range rxPos {
		total += arrivalTimes[i] - pos.Dist(rx)/C
	}
	return total / float64(len(rxPos))
}

func runLevenbergMarquardtTOA(startPos Vec3, startEmission float64, rxPos []Vec3, arrivalTimes []float64) (Vec3, float64, float64, error) {
	pos := startPos
	emission := startEmission
	lambda := 1e-3
	prevCost := math.MaxFloat64

	for iter := 0; iter < 250; iter++ {
		residuals, jacobian := computeTOAResidualAndJacobian(pos, emission, rxPos, arrivalTimes)
		cost := sumSquares(residuals)
		if math.Abs(prevCost-cost) < 1e-18 {
			return pos, emission, cost, nil
		}
		prevCost = cost

		var JtJ [4][4]float64
		var Jtr [4]float64
		for i, r := range residuals {
			for a := 0; a < 4; a++ {
				Jtr[a] += jacobian[i][a] * r
				for b := 0; b < 4; b++ {
					JtJ[a][b] += jacobian[i][a] * jacobian[i][b]
				}
			}
		}
		for i := 0; i < 4; i++ {
			JtJ[i][i] *= (1 + lambda)
		}

		delta, err := solve4x4(JtJ, Jtr)
		if err != nil {
			lambda *= 10
			continue
		}

		newPos := Vec3{pos.X - delta[0], pos.Y - delta[1], pos.Z - delta[2]}
		newEmission := emission - delta[3]
		newResiduals, _ := computeTOAResidualAndJacobian(newPos, newEmission, rxPos, arrivalTimes)
		newCost := sumSquares(newResiduals)

		if newCost < cost {
			pos = newPos
			emission = newEmission
			lambda /= 3
		} else {
			lambda *= 3
		}
	}

	finalResiduals, _ := computeTOAResidualAndJacobian(pos, emission, rxPos, arrivalTimes)
	return pos, emission, sumSquares(finalResiduals), nil
}

func computeTOAResiduals(pos Vec3, emission float64, rxPos []Vec3, arrivalTimes []float64) []float64 {
	residuals, _ := computeTOAResidualAndJacobian(pos, emission, rxPos, arrivalTimes)
	return residuals
}

func computeTOAResidualAndJacobian(pos Vec3, emission float64, rxPos []Vec3, arrivalTimes []float64) ([]float64, [][4]float64) {
	residuals := make([]float64, len(rxPos))
	jacobian := make([][4]float64, len(rxPos))

	for i, rx := range rxPos {
		dist := pos.Dist(rx)
		predicted := emission + dist/C
		residuals[i] = predicted - arrivalTimes[i]

		if dist > 0 {
			jacobian[i][0] = (pos.X - rx.X) / (C * dist)
			jacobian[i][1] = (pos.Y - rx.Y) / (C * dist)
			jacobian[i][2] = (pos.Z - rx.Z) / (C * dist)
		}
		jacobian[i][3] = 1
	}

	return residuals, jacobian
}

func pickInliers(residuals []float64, threshold float64) []int {
	inliers := make([]int, 0, len(residuals))
	for i, residual := range residuals {
		if math.Abs(residual) <= threshold {
			inliers = append(inliers, i)
		}
	}
	return inliers
}

func residualMetrics(residuals []float64, indices []int) (sumSq float64, rms float64) {
	for _, idx := range indices {
		sumSq += residuals[idx] * residuals[idx]
	}
	if len(indices) == 0 {
		return math.MaxFloat64, math.MaxFloat64
	}
	return sumSq, math.Sqrt(sumSq / float64(len(indices)))
}

func betterCandidate(a, b fitCandidate) bool {
	if len(a.inliers) != len(b.inliers) {
		return len(a.inliers) > len(b.inliers)
	}
	if math.Abs(a.rms-b.rms) > 1e-12 {
		return a.rms < b.rms
	}
	return a.sumSq < b.sumSq
}

func combinationIndices(n, k int) [][]int {
	if k <= 0 || n < k {
		return nil
	}
	var result [][]int
	indices := make([]int, k)
	var build func(start, depth int)
	build = func(start, depth int) {
		if depth == k {
			combination := append([]int(nil), indices...)
			result = append(result, combination)
			return
		}
		for i := start; i <= n-(k-depth); i++ {
			indices[depth] = i
			build(i+1, depth+1)
		}
	}
	build(0, 0)
	return result
}

func makeSequentialIndices(n int) []int {
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

func selectVec3s(values []Vec3, indices []int) []Vec3 {
	selected := make([]Vec3, len(indices))
	for i, idx := range indices {
		selected[i] = values[idx]
	}
	return selected
}

func selectFloat64s(values []float64, indices []int) []float64 {
	selected := make([]float64, len(indices))
	for i, idx := range indices {
		selected[i] = values[idx]
	}
	return selected
}

func sumSquares(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value * value
	}
	return total
}

func residualMapForInliers(obs []Observation, inliers []int, residuals []float64) map[int64]float64 {
	out := make(map[int64]float64, len(inliers))
	for _, idx := range inliers {
		if idx >= 0 && idx < len(obs) && idx < len(residuals) {
			out[obs[idx].SensorID] = residuals[idx]
		}
	}
	return out
}

func contributorsForInliers(obs []Observation, inliers []int, residuals []float64) []SensorContribution {
	contributors := make([]SensorContribution, 0, len(inliers))
	for _, idx := range inliers {
		if idx < 0 || idx >= len(obs) || idx >= len(residuals) {
			continue
		}
		observation := obs[idx]
		contributors = append(contributors, SensorContribution{
			SensorID:   observation.SensorID,
			SensorName: observation.SensorName,
			Lat:        observation.SensorLat,
			Lon:        observation.SensorLon,
			Alt:        observation.SensorAlt,
			ResidualM:  math.Abs(residuals[idx]) * C,
		})
	}

	sort.Slice(contributors, func(i, j int) bool {
		if contributors[i].ResidualM != contributors[j].ResidualM {
			return contributors[i].ResidualM < contributors[j].ResidualM
		}
		return contributors[i].SensorID < contributors[j].SensorID
	})
	return contributors
}

func estimateUncertaintyMeters(rmsResidualM, gdop float64, sensors int) float64 {
	uncertainty := rmsResidualM * 1.8
	if !math.IsNaN(gdop) && !math.IsInf(gdop, 0) {
		uncertainty += 120 * gdop
	} else {
		uncertainty += 1500
	}
	if sensors > 0 {
		uncertainty /= math.Sqrt(float64(sensors) / float64(MinSensors))
	}
	return clampFloat(uncertainty, 100, 25000)
}

func computeQualityScore(rmsResidualM, gdop float64, sensors int) float64 {
	sensorScore := clampFloat((float64(sensors)-float64(MinSensors)+1)/4, 0, 1)
	residualScore := clampFloat(1-rmsResidualM/3500, 0, 1)
	geometryScore := 0.2
	if !math.IsNaN(gdop) && !math.IsInf(gdop, 0) {
		geometryScore = clampFloat(1-(gdop-1.5)/8.5, 0, 1)
	}
	return 100 * (0.35*sensorScore + 0.4*residualScore + 0.25*geometryScore)
}

func qualityLabel(score float64) string {
	switch {
	case score >= 80:
		return "HIGH"
	case score >= 55:
		return "MED"
	default:
		return "LOW"
	}
}

func computeGDOP(pos Vec3, rxPos []Vec3) float64 {
	if len(rxPos) < MinSensors {
		return math.Inf(1)
	}

	var gtg [4][4]float64
	for _, rx := range rxPos {
		line := pos.Sub(rx)
		dist := line.Norm()
		if dist == 0 {
			return math.Inf(1)
		}
		row := [4]float64{line.X / dist, line.Y / dist, line.Z / dist, 1}
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				gtg[i][j] += row[i] * row[j]
			}
		}
	}

	inv, err := invert4x4(gtg)
	if err != nil {
		return math.Inf(1)
	}

	trace := inv[0][0] + inv[1][1] + inv[2][2] + inv[3][3]
	if trace <= 0 || math.IsNaN(trace) {
		return math.Inf(1)
	}
	return math.Sqrt(trace)
}

func invert4x4(A [4][4]float64) ([4][4]float64, error) {
	var aug [4][8]float64
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			aug[r][c] = A[r][c]
		}
		aug[r][4+r] = 1
	}

	for col := 0; col < 4; col++ {
		pivot := col
		for row := col + 1; row < 4; row++ {
			if math.Abs(aug[row][col]) > math.Abs(aug[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(aug[pivot][col]) < 1e-12 {
			return [4][4]float64{}, fmt.Errorf("singular matrix")
		}
		if pivot != col {
			aug[col], aug[pivot] = aug[pivot], aug[col]
		}

		pivotValue := aug[col][col]
		for c := col; c < 8; c++ {
			aug[col][c] /= pivotValue
		}

		for row := 0; row < 4; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			for c := col; c < 8; c++ {
				aug[row][c] -= factor * aug[col][c]
			}
		}
	}

	var inv [4][4]float64
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			inv[r][c] = aug[r][c+4]
		}
	}
	return inv, nil
}

func solve4x4(A [4][4]float64, b [4]float64) ([4]float64, error) {
	var aug [4][5]float64
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			aug[r][c] = A[r][c]
		}
		aug[r][4] = b[r]
	}

	for col := 0; col < 4; col++ {
		pivot := col
		for row := col + 1; row < 4; row++ {
			if math.Abs(aug[row][col]) > math.Abs(aug[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(aug[pivot][col]) < 1e-12 {
			return [4]float64{}, fmt.Errorf("singular matrix")
		}
		if pivot != col {
			aug[col], aug[pivot] = aug[pivot], aug[col]
		}

		pivotValue := aug[col][col]
		for c := col; c < 5; c++ {
			aug[col][c] /= pivotValue
		}

		for row := 0; row < 4; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			for c := col; c < 5; c++ {
				aug[row][c] -= factor * aug[col][c]
			}
		}
	}

	return [4]float64{aug[0][4], aug[1][4], aug[2][4], aug[3][4]}, nil
}
