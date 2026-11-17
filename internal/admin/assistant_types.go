package admin

import (
	"encoding/json"
	"sync"
	"time"
)

// Error codes of the AI assistant. They travel in the "code" field of an error
// answer and in the error event of a turn.
const (
	aiErrDisabled        = "ai_disabled"
	aiErrBadKey          = "ai_bad_key"
	aiErrRateLimited     = "ai_rate_limited"
	aiErrProvider        = "ai_provider_error"
	aiErrTimeout         = "ai_timeout"
	aiErrStale           = "ai_stale"
	aiErrConflict        = "ai_conflict"
	aiErrBusy            = "ai_busy"
	aiErrRedactedValue   = "ai_redacted_value"
	aiErrDiskFull        = "ai_disk_full"
	aiErrQuota           = "ai_quota"
	aiErrConfirmRequired = "ai_confirm_required"
	aiErrHasLinks        = "ai_has_links"
	aiErrLandingTooLarge = "ai_landing_too_large"
	aiErrBadModel        = "ai_bad_model"
	aiErrBadLimit        = "ai_bad_limit"
	aiErrBadRequest      = "ai_bad_request"
	aiErrKeitaroRunning  = "keitaro_running"
	aiErrResultTooLarge  = "result_too_large"
)

// Operations of a proposed change.
const (
	aiOpCreate = "create"
	aiOpUpdate = "update"
	aiOpDelete = "delete"
)

// Statuses of a recorded change.
const (
	// aiChangePending marks a change whose write has started and is not confirmed.
	aiChangePending = "pending"
	// aiChangeDone marks an applied change that can be reverted.
	aiChangeDone = "done"
	// aiChangeAborted marks a change whose write did not happen.
	aiChangeAborted = "aborted"
	// aiChangeReverted marks a change that was rolled back.
	aiChangeReverted = "reverted"
)

// Statuses of a chat.
const (
	aiChatIdle        = "idle"
	aiChatRunning     = "running"
	aiChatInterrupted = "interrupted"
)

// Roles of a chat message.
const (
	aiRoleUser      = "user"
	aiRoleAssistant = "assistant"
)

// aiPrice is the price of one model in US dollars per million tokens.
type aiPrice struct {
	InputPerM  float64 `json:"input_per_m"`
	OutputPerM float64 `json:"output_per_m"`
}

// aiSettings is the content of ai.json.
//
// Keys holds the provider keys by provider id and never leaves the server.
// The four daily limit fields are pointers: nil means the built-in default, aiNoLimit means
// no limit and zero refuses every call the limit counts. Prices overrides the built-in price table by
// model id. UserBudgetUSD and GroupBudgetUSD are the USD budgets of the current day by usage id of
// an account and by role id (the group of users); a missing id means no budget, and they do not
// replace the four limits above: the stricter one refuses the call.
type aiSettings struct {
	Enabled         bool               `json:"enabled"`
	Provider        string             `json:"provider"`
	Keys            map[string]string  `json:"keys,omitempty"`
	Model           string             `json:"model"`
	AllowedModels   []string           `json:"allowed_models"`
	MaxOutputTokens int                `json:"max_output_tokens"`
	MaxSteps        int                `json:"max_steps"`
	TurnInputTokens int                `json:"turn_input_tokens"`
	DailyTokens     *int64             `json:"daily_tokens,omitempty"`
	UserDailyTokens *int64             `json:"user_daily_tokens,omitempty"`
	DailyUSD        *float64           `json:"daily_usd,omitempty"`
	UserDailyUSD    *float64           `json:"user_daily_usd,omitempty"`
	UserBudgetUSD   map[string]float64 `json:"user_budget_usd,omitempty"`
	GroupBudgetUSD  map[string]float64 `json:"group_budget_usd,omitempty"`
	Prices          map[string]aiPrice `json:"prices,omitempty"`
	Perms           aiPerms            `json:"perms"`
}

// aiProposal is a change the model asked for. It is stored, not executed: the
// user applies it from the card.
//
// Patch holds only the changed fields; BeforeView is the record as the user
// sees it, and BeforeHash is the hash of the stored record the proposal was
// built against. Board is fixed at proposal time and used again on apply.
type aiProposal struct {
	ID         string          `json:"id"`
	ChatID     string          `json:"chat_id"`
	MessageID  string          `json:"message_id"`
	UID        string          `json:"uid"`
	Tool       string          `json:"tool"`
	Resource   string          `json:"resource"`
	EntityID   string          `json:"entity_id"`
	Op         string          `json:"op"`
	Board      string          `json:"board,omitempty"`
	Patch      json.RawMessage `json:"patch,omitempty"`
	BeforeView json.RawMessage `json:"before_view,omitempty"`
	BeforeHash string          `json:"before_hash,omitempty"`
	Created    time.Time       `json:"created"`
}

// aiChange is the record of one applied (or started) proposal and the data a
// revert needs.
//
// Before is the raw stored record before the write, kept with owner, board and
// hidden fields. AfterHash is the hash of the record after the write; a revert
// is allowed only while the record still has it. Blob names the archive of the
// landing files under the assistant directory and is empty for other resources.
type aiChange struct {
	ID         string          `json:"id"`
	ProposalID string          `json:"proposal_id"`
	ChatID     string          `json:"chat_id"`
	MessageID  string          `json:"message_id"`
	UID        string          `json:"uid"`
	Resource   string          `json:"resource"`
	EntityID   string          `json:"entity_id"`
	Op         string          `json:"op"`
	Board      string          `json:"board,omitempty"`
	Status     string          `json:"status"`
	Before     json.RawMessage `json:"before,omitempty"`
	AfterHash  string          `json:"after_hash,omitempty"`
	Blob       string          `json:"blob,omitempty"`
	Created    time.Time       `json:"created"`
	Applied    time.Time       `json:"applied,omitempty"`
	Reverted   time.Time       `json:"reverted,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// aiMessage is one entry of a chat.
//
// Role is aiRoleUser or aiRoleAssistant. Lang is the language key (en, ru or uk) the user asked
// the answer in; it is kept on user messages and never changes the stored text. Proposals lists
// the ids of the proposals the message produced. Tokens counts the provider usage of the turn
// that produced an assistant message.
type aiMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Lang      string    `json:"lang,omitempty"`
	Model     string    `json:"model,omitempty"`
	Proposals []string  `json:"proposals,omitempty"`
	Tokens    int64     `json:"tokens,omitempty"`
	Error     string    `json:"error,omitempty"`
	Created   time.Time `json:"created"`
}

// aiChat is the stored history of one conversation of one user.
type aiChat struct {
	ID       string      `json:"id"`
	UID      string      `json:"uid"`
	Title    string      `json:"title"`
	Status   string      `json:"status"`
	Created  time.Time   `json:"created"`
	Updated  time.Time   `json:"updated"`
	Messages []aiMessage `json:"messages"`
}

// aiRange is the reporting period shown on the page the user asks from.
type aiRange struct {
	ID   string `json:"id,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// aiPageContext is what the panel sends about the page the user is on. The
// server keeps only these keys and treats every value as untrusted data.
type aiPageContext struct {
	Page     string   `json:"page,omitempty"`
	Arg      string   `json:"arg,omitempty"`
	Board    string   `json:"board,omitempty"`
	Range    aiRange  `json:"range"`
	Filters  []string `json:"filters,omitempty"`
	Tab      string   `json:"tab,omitempty"`
	Campaign string   `json:"campaign,omitempty"`
}

// assistantState is the in-memory state of the assistant inside Server.
// mu guards the fields the assistant files add to it.
type assistantState struct {
	mu sync.Mutex
	// applyMu serializes apply, revert, reject, recovery and rotation of changes. It is the
	// outermost lock of a change: taken before the Keitaro write gate, groupsMu and any store lock.
	applyMu sync.Mutex
	// changesMu guards assistant/changes.json. It is a leaf lock: nothing else is locked while it
	// is held, so a write callback may take it under a store lock.
	changesMu sync.Mutex
	// usage is the token and USD counter of the current UTC day, see aiReserve and aiCommit.
	usage aiUsageState
	// models holds the provider model catalogs, see assistant_models.go.
	models aiModelsState
	// chats is the lock and the tunables of the chat files, see assistant_chats.go.
	chats aiChatStore
	// turns is the registry of the turns that run, see assistant_turn.go.
	turns aiTurnState
}
