package pipeline

import "time"

func NextRetryAt(now time.Time, attempts, baseSeconds, maxSeconds int) time.Time {
	if attempts <= 0 {
		attempts = 1
	}
	if baseSeconds <= 0 {
		baseSeconds = 1
	}
	if maxSeconds <= 0 {
		maxSeconds = baseSeconds
	}

	delay := baseSeconds
	for i := 1; i < attempts; i++ {
		if delay >= maxSeconds/2 {
			delay = maxSeconds
			break
		}
		delay *= 2
	}
	if delay > maxSeconds {
		delay = maxSeconds
	}
	return now.Add(time.Duration(delay) * time.Second)
}
