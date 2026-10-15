package service

func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i]
		}
		count++
	}
	return s
}
