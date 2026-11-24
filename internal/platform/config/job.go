package config

import "time"

// The defaults of the job.* keys (ADR-0347 Contracts): job.maxRunning and job.payloadLimit are at least 1, and
// job.retention is a positive duration.
const (
	DefaultJobMaxRunning   = 4
	DefaultJobPayloadLimit = 256 << 10
	DefaultJobRetention    = 720 * time.Hour
)

// JobRetention is job.retention parsed, how long an ended JobRun is kept: DefaultJobRetention when empty; fault.Invalid
// naming the key for a value that is no ADR-0194 duration or is not positive.
func (c Config) JobRetention() (time.Duration, error) {
	return boundedDuration("config.JobRetention", "job.retention", c.Job.Retention, DefaultJobRetention, time.Millisecond)
}

// CheckJob refuses a job.retention that JobRetention refuses; the struct tags hold the other job.* keys to at least 1.
func (c Config) CheckJob() []Finding {
	var f findings
	if _, err := c.JobRetention(); err != nil {
		f.refuse("job.retention", "%v", err)
	}
	return f.errs
}
