package config

import "testing"

func TestWorkerLimits(t *testing.T) {
	for _, tt := range []struct {
		name, worker, endpoint string
		want                   WorkerLimits
		bad                    bool
	}{
		{"defaults", "", "", WorkerLimits{4, 1}, false},
		{"override", "8", "2", WorkerLimits{8, 2}, false},
		{"serial debugging", "1", "1", WorkerLimits{1, 1}, false},
		{"zero", "0", "1", WorkerLimits{}, true},
		{"negative", "4", "-1", WorkerLimits{}, true},
		{"malformed", "many", "1", WorkerLimits{}, true},
		{"overflow", "999999999999999999999", "1", WorkerLimits{}, true},
		{"endpoint zero", "4", "0", WorkerLimits{}, true},
		{"endpoint exceeds total", "2", "3", WorkerLimits{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{"WORKER_CONCURRENCY": tt.worker, "ENDPOINT_CONCURRENCY": tt.endpoint}
			got, err := loadWorkerLimits(func(key string) string { return values[key] })
			if (err != nil) != tt.bad || !tt.bad && got != tt.want {
				t.Fatalf("limits=%+v err=%v", got, err)
			}
		})
	}
}
