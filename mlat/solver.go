package mlat

import (
	"fmt"
	"math"
	"sort"
)

const C = 299_702_547.0


type Vec3 struct{ X, Y, Z float64 }

func (a Vec3) Sub(b Vec3) Vec3      { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Add(b Vec3) Vec3      { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Scale(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Norm() float64        { return math.Sqrt(a.X*a.X + a.Y*a.Y + a.Z*a.Z) }
func (a Vec3) Dist(b Vec3) float64  { return a.Sub(b).Norm() }

func GeodeticToECEF(lat, lon, alt float64) Vec3 {
	const (
		a  = 6378137.0        // WGS84 semi-major axis
		e2 = 0.00669437999014 // WGS84 first eccentricity squared
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


func Solve(icao string, obs []Observation) (*MLATResult, error) {
	if len(obs) < MinSensors {
		return nil, fmt.Errorf("need at least %d sensors, got %d", MinSensors, len(obs))
	}

	sort.Slice(obs, func(i, j int) bool {
		return obs[i].TimestampNs() < obs[j].TimestampNs()
	})

	rxPos := make([]Vec3, len(obs))
	for i, o := range obs {
		rxPos[i] = GeodeticToECEF(o.SensorLat, o.SensorLon, o.SensorAlt)
	}

	refT := obs[0].TimestampNs()
	tdoas := make([]float64, len(obs))
	for i, o := range obs {
		tdoas[i] = float64(o.TimestampNs()-refT) * 1e-9 // convert ns to seconds
	}

	var cx, cy, cz float64
	for _, r := range rxPos {
		cx += r.X
		cy += r.Y
		cz += r.Z
	}
	n := float64(len(rxPos))
	centroid := Vec3{cx / n, cy / n, cz / n}
	norm := centroid.Norm()
	startAltitudes := []float64{1000, 3000, 6000, 10000, 12000}
	bestPos := Vec3{}
	bestCost := math.MaxFloat64
	converged := false
	for _, altGuess := range startAltitudes {
		start := centroid.Scale((norm + altGuess) / norm)
		pos, cost, err := runLevenbergMarquardt(start, rxPos, tdoas)
		if err != nil {
			continue
		}
		if cost < bestCost {
			bestPos = pos
			bestCost = cost
			converged = true
		}
	}

	if !converged {
		return nil, fmt.Errorf("solver failed to converge for %s", icao)
	}

	// Final residual
	finalRes, _ := computeResidualAndJacobian(bestPos, rxPos, tdoas)
	finalCost := 0.0
	for _, r := range finalRes {
		finalCost += r * r
	}

	if finalCost > 1e-4 {
		return nil, fmt.Errorf("solver did not converge for %s (cost=%.6f)", icao, finalCost)
	}

	lat, lon, alt := ECEFToGeodetic(bestPos)

	if alt < -500 || alt > 15000 {
		return nil, fmt.Errorf("unrealistic altitude %.0fm for %s", alt, icao)
	}

	return &MLATResult{
		ICAO:       icao,
		Lat:        lat,
		Lon:        lon,
		Alt:        alt,
		Cost:       finalCost,
		NumSensors: len(obs),
	}, nil
}

func runLevenbergMarquardt(start Vec3, rxPos []Vec3, tdoas []float64) (Vec3, float64, error) {
	pos := start
	lambda := 1e-3
	prevCost := math.MaxFloat64

	for iter := 0; iter < 250; iter++ {
		residuals, J := computeResidualAndJacobian(pos, rxPos, tdoas)
		cost := sumSquares(residuals)
		if math.Abs(prevCost-cost) < 1e-16 {
			return pos, cost, nil
		}
		prevCost = cost

		JtJ := [3][3]float64{}
		Jtr := [3]float64{}
		for i, r := range residuals {
			for a := 0; a < 3; a++ {
				Jtr[a] += J[i][a] * r
				for b := 0; b < 3; b++ {
					JtJ[a][b] += J[i][a] * J[i][b]
				}
			}
		}

		for a := 0; a < 3; a++ {
			JtJ[a][a] *= (1 + lambda)
		}

		delta, err := solve3x3(JtJ, Jtr)
		if err != nil {
			lambda *= 10
			continue
		}

		newPos := Vec3{pos.X - delta[0], pos.Y - delta[1], pos.Z - delta[2]}
		newRes, _ := computeResidualAndJacobian(newPos, rxPos, tdoas)
		newCost := sumSquares(newRes)

		if newCost < cost {
			pos = newPos
			lambda /= 3
		} else {
			lambda *= 3
		}
	}

	return pos, sumSquaresFromPosition(pos, rxPos, tdoas), nil
}

func sumSquares(residuals []float64) float64 {
	total := 0.0
	for _, r := range residuals {
		total += r * r
	}
	return total
}

func sumSquaresFromPosition(pos Vec3, rxPos []Vec3, tdoas []float64) float64 {
	residuals, _ := computeResidualAndJacobian(pos, rxPos, tdoas)
	return sumSquares(residuals)
}

func computeResidualAndJacobian(pos Vec3, rxPos []Vec3, tdoas []float64) ([]float64, [][3]float64) {
	ref := rxPos[0]
	dRef := pos.Dist(ref)

	residuals := make([]float64, 0, len(rxPos)-1)
	J := make([][3]float64, 0, len(rxPos)-1)

	for i := 1; i < len(rxPos); i++ {
		rx := rxPos[i]
		dI := pos.Dist(rx)

		predicted := (dI - dRef) / C
		residuals = append(residuals, predicted-tdoas[i])

		var jRow [3]float64
		if dI > 0 && dRef > 0 {
			jRow[0] = (pos.X-rx.X)/(C*dI) - (pos.X-ref.X)/(C*dRef)
			jRow[1] = (pos.Y-rx.Y)/(C*dI) - (pos.Y-ref.Y)/(C*dRef)
			jRow[2] = (pos.Z-rx.Z)/(C*dI) - (pos.Z-ref.Z)/(C*dRef)
		}
		J = append(J, jRow)
	}
	return residuals, J
}

func solve3x3(A [3][3]float64, b [3]float64) ([3]float64, error) {
	det := A[0][0]*(A[1][1]*A[2][2]-A[1][2]*A[2][1]) -
		A[0][1]*(A[1][0]*A[2][2]-A[1][2]*A[2][0]) +
		A[0][2]*(A[1][0]*A[2][1]-A[1][1]*A[2][0])

	if math.Abs(det) < 1e-12 {
		return [3]float64{}, fmt.Errorf("singular matrix")
	}

	var x [3]float64
	for i := 0; i < 3; i++ {
		var Ai [3][3]float64
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				if c == i {
					Ai[r][c] = b[r]
				} else {
					Ai[r][c] = A[r][c]
				}
			}
		}
		di := Ai[0][0]*(Ai[1][1]*Ai[2][2]-Ai[1][2]*Ai[2][1]) -
			Ai[0][1]*(Ai[1][0]*Ai[2][2]-Ai[1][2]*Ai[2][0]) +
			Ai[0][2]*(Ai[1][0]*Ai[2][1]-Ai[1][1]*Ai[2][0])
		x[i] = di / det
	}
	return x, nil
}
