package relay

import "time"

// defaultBackoff 默认重试间隔：1s/2s/4s/8s/16s。
var defaultBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

// Backoff 返回默认策略下第 retry 次重试前的等待时间。
func Backoff(retry int) time.Duration {
	return BackoffFor(defaultBackoff, retry)
}

// BackoffFor 按配置的间隔序列返回第 retry 次重试前的等待时间。
// 序列耗尽后使用最后一个值；空序列时回退到默认策略。
func BackoffFor(sequence []time.Duration, retry int) time.Duration {
	if len(sequence) == 0 {
		sequence = defaultBackoff
	}
	if retry < 1 {
		retry = 1
	}
	index := retry - 1
	if index >= len(sequence) {
		index = len(sequence) - 1
	}
	return sequence[index]
}
