package models

import (
	"time"

	"gorm.io/gorm"
)

// JobKind is the type of work a queued game job performs.
type JobKind string

const (
	// JobKindConfig generates the game's VM configuration.
	JobKindConfig JobKind = "config"
	// JobKindMetadata retrieves the game's metadata from the metadata source.
	JobKindMetadata JobKind = "metadata"
	// JobKindLibrary processes the game files in the library: it registers the
	// games it finds and queues a config and a metadata job for each of them.
	JobKindLibrary JobKind = "library"
)

// LibraryJobGameID is the game id of library-wide jobs, which are not bound
// to a single game.
const LibraryJobGameID = 0

// JobStatus is the lifecycle state of a queued game job.
type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusProcessing JobStatus = "processing"
	JobStatusDone       JobStatus = "done"
	JobStatusFailed     JobStatus = "failed"
)

// GameJob is a unit of queued work against a game. The queue lives in
// PostgreSQL so any number of workers can share it.
type GameJob struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

	GameID   uint      `gorm:"index;not null" json:"game_id"`
	Kind     JobKind   `gorm:"not null" json:"kind"`
	Status   JobStatus `gorm:"not null;default:'pending';index" json:"status"`
	Payload  string    `gorm:"not null;default:''" json:"payload"` // JSON request payload (optional)
	Result   string    `gorm:"not null;default:''" json:"result"`  // JSON result once done
	Error    string    `gorm:"not null;default:''" json:"error"`
	Attempts int       `gorm:"not null;default:0" json:"attempts"`
}
