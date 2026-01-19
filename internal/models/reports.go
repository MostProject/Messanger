package models

import "time"

// SchemaRequestMessage sent to user asking for database schema
type SchemaRequestMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	RequestID                string `json:"RequestId"`
	OriginalQuery            string `json:"OriginalQuery"`
	Instructions             string `json:"Instructions"`
}

// SchemaResponseMessage from user containing database schema
type SchemaResponseMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	RequestID                string `json:"RequestId"`
	DatabaseType             string `json:"DatabaseType"`
	TablesAndColumns         string `json:"TablesAndColumns"`
	AdditionalInfo           string `json:"AdditionalInfo"`
}

// ReportModificationMessage from client to modify existing report
type ReportModificationMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	OriginalReportID         string `json:"OriginalReportId"`
	ModificationQuery        string `json:"ModificationQuery"`
	Instructions             string `json:"Instructions"`
}

// ReportPlaceholderMessage sent while report is being generated
type ReportPlaceholderMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	RequestID                string `json:"RequestId"`
	StatusMessage            string `json:"StatusMessage"`
	ProgressPercentage       int    `json:"ProgressPercentage"`
	CurrentStep              string `json:"CurrentStep"`
}

// ReportRequestMessage from user to get the generated report
type ReportRequestMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	RequestID                string `json:"RequestId"`
}

// ReportMessage containing generated report
type ReportMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	RequestID                string `json:"RequestId"`
	ReportQuery              string `json:"ReportQuery"`
	RequiredTables           string `json:"RequiredTables"`
	WebApplication           string `json:"WebApplication"`
	IsSuccessful             bool   `json:"IsSuccessful"`
	ErrorMessage             string `json:"ErrorMessage,omitempty"`
	ProcessingTimeMs         int64  `json:"ProcessingTimeMs"`
}

// ReportJob represents a queued report generation job
type ReportJob struct {
	JobID         string    `json:"jobId" dynamodbav:"JobId"`
	UserID        int       `json:"userId" dynamodbav:"UserId"`
	ConnectionID  string    `json:"connectionId" dynamodbav:"ConnectionId"`
	RequestID     string    `json:"requestId" dynamodbav:"RequestId"`
	Query         string    `json:"query" dynamodbav:"Query"`
	Schema        string    `json:"schema,omitempty" dynamodbav:"Schema,omitempty"`
	Status        JobStatus `json:"status" dynamodbav:"Status"`
	CreatedAt     time.Time `json:"createdAt" dynamodbav:"CreatedAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty" dynamodbav:"StartedAt,omitempty"`
	CompletedAt   *time.Time `json:"completedAt,omitempty" dynamodbav:"CompletedAt,omitempty"`
	Result        string    `json:"result,omitempty" dynamodbav:"Result,omitempty"`
	Error         string    `json:"error,omitempty" dynamodbav:"Error,omitempty"`
	Retries       int       `json:"retries" dynamodbav:"Retries"`
}

// JobStatus represents the status of a report job
type JobStatus string

const (
	JobStatusPending    JobStatus = "PENDING"
	JobStatusProcessing JobStatus = "PROCESSING"
	JobStatusCompleted  JobStatus = "COMPLETED"
	JobStatusFailed     JobStatus = "FAILED"
)
