package vector

func ManhattanDistance[T Calculable](a, b [ANNOYLIB_V_ARRAY_SIZE]T, vectorLength int) T {
	var sum T
	for i := 0; i < vectorLength; i++ {
		sum += Abs(a[i] - b[i])
	}
	return sum
}
