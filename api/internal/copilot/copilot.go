package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// MaxHistoryWindow, LLM bağlamına giren en fazla geçmiş mesaj sayısı
// (sözleşme: "Geçmiş penceresi: son 20 mesaj"). Çağıran (api handler'ı)
// store.ListChat'i bu limitle çağırır; burada ayrıca kırpılır (savunmacı).
const MaxHistoryWindow = 20

// maxSuggestions, sohbet cevabındaki öneri çipi üst sınırı.
const maxSuggestions = 3

// ErrEmptyResponse, LLM'in boş/anlamsız cevap döndüğünü belirtir — api
// katmanı bunu 502 upstream'e çevirir.
var ErrEmptyResponse = errors.New("copilot: LLM boş cevap döndü")

// ErrInvalidDraft, blend cevabının sözleşme sınırlarının (title/problem/
// solution uzunluğu, domain_tags sayısı) dışında kaldığını belirtir — kart
// YAZILMAZ, api katmanı 502 upstream döner.
var ErrInvalidDraft = errors.New("copilot: blend taslağı sözleşme sınırlarının dışında")

// ChatResult, kart sohbeti cevabı.
type ChatResult struct {
	Reply       string
	Suggestions []string
}

// Chat, kart sohbetinde tek tur yanıt üretir. history, `msg`'DEN ÖNCEKİ
// (son MaxHistoryWindow ile sınırlı) geçmiştir — msg ayrıca eklenir.
func Chat(ctx context.Context, chat llm.Chat, idea *store.Idea, history []store.ChatMessage, msg, lang string) (ChatResult, error) {
	system := ChatSystemPrompt(lang)
	user := ChatUserPrompt(idea, clipHistory(history), msg)

	raw, err := chat.ChatJSON(ctx, system, user)
	if err != nil {
		return ChatResult{}, fmt.Errorf("copilot chat: %w", err)
	}
	return parseChatResponse(raw)
}

type chatResponse struct {
	Reply       string   `json:"reply"`
	Suggestions []string `json:"suggestions"`
}

// parseChatResponse, LLM cevabını savunmacı ayrıştırır: boş cevap veya boş
// "reply" alanı ErrEmptyResponse döner (api katmanı 502'ye çevirir);
// "suggestions" eksikse/boşsa sessizce [] olur, en fazla 3 tanesi tutulur.
func parseChatResponse(raw string) (ChatResult, error) {
	if strings.TrimSpace(raw) == "" {
		return ChatResult{}, ErrEmptyResponse
	}

	var r chatResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return ChatResult{}, fmt.Errorf("%w: LLM yanıtı JSON değil: %v (yanıt: %.200s)", ErrEmptyResponse, err, raw)
	}

	reply := strings.TrimSpace(r.Reply)
	if reply == "" {
		return ChatResult{}, ErrEmptyResponse
	}

	suggestions := []string{}
	for _, s := range r.Suggestions {
		if t := strings.TrimSpace(s); t != "" {
			suggestions = append(suggestions, t)
		}
		if len(suggestions) == maxSuggestions {
			break
		}
	}

	return ChatResult{Reply: reply, Suggestions: suggestions}, nil
}

// Blend, kart + sohbet geçmişinden yeni bir kart taslağı üretir. Doğrulama
// başarısız olursa (title/problem/solution uzunluğu, domain_tags sayısı
// sözleşme dışı) ErrInvalidDraft döner — kart YAZILMAZ.
func Blend(ctx context.Context, chat llm.Chat, idea *store.Idea, history []store.ChatMessage, lang string) (store.BlendDraft, error) {
	system := BlendSystemPrompt(lang)
	user := BlendUserPrompt(idea, clipHistory(history))

	raw, err := chat.ChatJSON(ctx, system, user)
	if err != nil {
		return store.BlendDraft{}, fmt.Errorf("copilot blend: %w", err)
	}
	return parseBlendResponse(raw)
}

type blendResponse struct {
	Title              string   `json:"title"`
	ProblemStatement   string   `json:"problem_statement"`
	ProposedSolution   string   `json:"proposed_solution"`
	TargetUser         string   `json:"target_user"`
	DomainTags         []string `json:"domain_tags"`
	UrgencyScore       int      `json:"urgency_score"`
	MonetizationSignal int      `json:"monetization_signal"`
}

const (
	titleMinLen   = 8
	titleMaxLen   = 120
	fieldMinLen   = 40
	maxDomainTags = 6
)

// parseBlendResponse, LLM cevabını savunmacı ayrıştırır ve sözleşme
// sınırlarına göre doğrular. Sınır dışı alan varsa kart YAZILMASIN diye
// ErrInvalidDraft döner (skorlar hariç — onlar clamp edilir, sözleşme
// yalnız title/problem/solution/domain_tags için sert sınır tanımlar).
func parseBlendResponse(raw string) (store.BlendDraft, error) {
	if strings.TrimSpace(raw) == "" {
		return store.BlendDraft{}, ErrEmptyResponse
	}

	var r blendResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return store.BlendDraft{}, fmt.Errorf("%w: LLM yanıtı JSON değil: %v (yanıt: %.200s)", ErrInvalidDraft, err, raw)
	}

	title := strings.TrimSpace(r.Title)
	problem := strings.TrimSpace(r.ProblemStatement)
	solution := strings.TrimSpace(r.ProposedSolution)
	target := strings.TrimSpace(r.TargetUser)

	if n := len([]rune(title)); n < titleMinLen || n > titleMaxLen {
		return store.BlendDraft{}, fmt.Errorf("%w: title uzunluğu %d (%d-%d beklenir)", ErrInvalidDraft, n, titleMinLen, titleMaxLen)
	}
	if n := len([]rune(problem)); n < fieldMinLen {
		return store.BlendDraft{}, fmt.Errorf("%w: problem_statement kısa (%d < %d)", ErrInvalidDraft, n, fieldMinLen)
	}
	if n := len([]rune(solution)); n < fieldMinLen {
		return store.BlendDraft{}, fmt.Errorf("%w: proposed_solution kısa (%d < %d)", ErrInvalidDraft, n, fieldMinLen)
	}

	tags := normalizeSlugs(r.DomainTags)
	if len(tags) == 0 || len(tags) > maxDomainTags {
		return store.BlendDraft{}, fmt.Errorf("%w: domain_tags sayısı %d (1-%d beklenir)", ErrInvalidDraft, len(tags), maxDomainTags)
	}

	return store.BlendDraft{
		Title:              title,
		ProblemStatement:   problem,
		ProposedSolution:   solution,
		TargetUser:         target,
		DomainTags:         tags,
		UrgencyScore:       clamp(r.UrgencyScore, 1, 5),
		MonetizationSignal: clamp(r.MonetizationSignal, 0, 5),
	}, nil
}

// clipHistory, geçmişi en yeni MaxHistoryWindow mesajla sınırlar (history
// kronolojik/eskiden yeniye sıralıdır — çağıran zaten bu sınırla getirmiş
// olsa da burada savunmacı tekrar kırpılır).
func clipHistory(history []store.ChatMessage) []store.ChatMessage {
	if len(history) <= MaxHistoryWindow {
		return history
	}
	return history[len(history)-MaxHistoryWindow:]
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeSlugs, tag'leri kanonik EN slug biçimine indirger, boş/tekrar
// eden tag'leri eler. Sayıyı SINIRLAMAZ (üst sınır çağıran tarafından
// doğrulanır — "sınır dışı" reddedilsin diye burada sessizce kırpılmaz).
func normalizeSlugs(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		s := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(t), "-"), "-")
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
