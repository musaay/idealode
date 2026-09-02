package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// testSID/testSID2, testlerde kullanılan geçerli (hex, 32 bayt) oturum
// kimlikleri — biri "kendi" oturum, diğeri "başkasının" oturumu.
const (
	testSID  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSID2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeStore, canlı veritabanı olmadan handler testleri için IdeaStore'u
// uygular.
type fakeStore struct {
	ideas       []store.Idea
	byID        map[int64]*store.Idea
	sources     map[int64][]store.IdeaSource
	lastFilter  store.IdeaFilter
	listErr     error
	sourcesErr  error
	forceGetErr error         // ErrNotFound dışı bir hata simüle etmek için
	delay       time.Duration // gerçek sunucuda zaman aşımını tetiklemek için (bkz. TestTimeout_RealServer)

	// kart sohbeti (Idea Copilot, #66)
	chat       map[int64]map[string][]store.ChatMessage // ideaID -> sid -> mesajlar
	chatErr    error
	appendErr  error
	blendErr   error
	nextMsgID  int64
	nextIdeaID int64
}

func (f *fakeStore) ListIdeasFiltered(ctx context.Context, filt store.IdeaFilter) ([]store.Idea, error) {
	f.lastFilter = filt
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []store.Idea{}
	for _, i := range f.ideas {
		if filt.SourceType != "" && i.SourceType != filt.SourceType {
			continue
		}
		// ai_blended görünürlük kuralı: yalnız üreten oturuma görünür.
		if i.SourceType == "ai_blended" && (filt.SessionID == "" || i.CreatedBySessionID != filt.SessionID) {
			continue
		}
		cp := i
		cp.Mine = cp.SourceType == "ai_blended" && cp.CreatedBySessionID == filt.SessionID
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakeStore) GetIdea(ctx context.Context, id int64, sid string) (*store.Idea, error) {
	if f.forceGetErr != nil {
		return nil, f.forceGetErr
	}
	idea, ok := f.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	if idea.SourceType == "ai_blended" && idea.CreatedBySessionID != sid {
		return nil, store.ErrNotFound
	}
	cp := *idea
	cp.Mine = cp.SourceType == "ai_blended" && cp.CreatedBySessionID == sid
	return &cp, nil
}

func (f *fakeStore) IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error) {
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return f.sources[ideaID], nil // kasıtlı: nil dönebilir (nil-slice testi)
}

func (f *fakeStore) ListChat(ctx context.Context, ideaID int64, sid string, limit int) ([]store.ChatMessage, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	msgs := f.chat[ideaID][sid]
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

func (f *fakeStore) AppendChat(ctx context.Context, ideaID int64, sid, role, message string) (store.ChatMessage, error) {
	if f.appendErr != nil {
		return store.ChatMessage{}, f.appendErr
	}
	if f.chat == nil {
		f.chat = map[int64]map[string][]store.ChatMessage{}
	}
	if f.chat[ideaID] == nil {
		f.chat[ideaID] = map[string][]store.ChatMessage{}
	}
	f.nextMsgID++
	m := store.ChatMessage{ID: f.nextMsgID, Role: role, Message: message, CreatedAt: time.Now()}
	f.chat[ideaID][sid] = append(f.chat[ideaID][sid], m)
	return m, nil
}

func (f *fakeStore) InsertBlendedIdea(ctx context.Context, parent *store.Idea, draft store.BlendDraft, sid string) (*store.Idea, error) {
	if f.blendErr != nil {
		return nil, f.blendErr
	}
	if f.nextIdeaID == 0 {
		f.nextIdeaID = 1000
	}
	f.nextIdeaID++
	parentID := parent.ID
	ni := &store.Idea{
		ID:                 f.nextIdeaID,
		Title:              draft.Title,
		ProblemStatement:   draft.ProblemStatement,
		ProposedSolution:   draft.ProposedSolution,
		TargetUser:         draft.TargetUser,
		EvidenceCount:      parent.EvidenceCount,
		ExampleQuotes:      parent.ExampleQuotes,
		SourceType:         "ai_blended",
		SourceThemeID:      parent.SourceThemeID,
		DomainTags:         draft.DomainTags,
		LocalEvidence:      parent.LocalEvidence,
		ParentIdeaID:       &parentID,
		CreatedBySessionID: sid,
		Mine:               true,
		UrgencyScore:       draft.UrgencyScore,
		MonetizationSignal: draft.MonetizationSignal,
		CreatedAt:          time.Now(),
	}
	f.byID[ni.ID] = ni
	f.ideas = append(f.ideas, *ni)
	return ni, nil
}

func newFakeStore() *fakeStore {
	idea1 := store.Idea{ID: 1, Title: "Kart 1", SourceType: "pain_point"}
	idea2 := store.Idea{ID: 2, Title: "Kart 2", SourceType: "market_derived"}
	idea3 := store.Idea{ID: 3, Title: "Kart 3 (blended)", SourceType: "ai_blended", CreatedBySessionID: testSID}
	return &fakeStore{
		ideas: []store.Idea{idea1, idea2, idea3},
		byID:  map[int64]*store.Idea{1: &idea1, 2: &idea2, 3: &idea3},
		sources: map[int64][]store.IdeaSource{
			1: {{Platform: "reddit", Community: "r/test", URL: "https://x", CreatedAt: time.Now()}},
		},
	}
}

// fakeLLM, llm.Chat'i sahte olarak uygular — canlı Groq'a hiç gitmez.
type fakeLLM struct {
	response string
	err      error
}

func (f *fakeLLM) ChatJSON(ctx context.Context, system, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

const fakeChatOK = `{"reply":"Bunu şöyle geliştirebilirsin.","suggestions":["Fiyatlandırmayı düşün","Rakipleri araştır"]}`
const fakeBlendOK = `{"title":"Türetilmiş Fikir Başlığı","problem_statement":"Bu yeterince uzun bir problem tanımıdır ve en az kırk karakter içerir.",` +
	`"proposed_solution":"Bu da yeterince uzun bir çözüm tanımıdır ve kırk karakteri geçer.","target_user":"küçük işletmeler",` +
	`"domain_tags":["kobi","saas"],"urgency_score":4,"monetization_signal":3}`

func newTestServer(ideas IdeaStore, chat *fakeLLM) *Server {
	return NewServer(ideas, chat)
}

func doReq(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doReqSID(t, h, http.MethodGet, path, testSID, nil)
}

func doReqSID(t *testing.T, h http.Handler, method, path, sid string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if sid != "" {
		r.Header.Set("X-Session-Id", sid)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHealthz(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	assertContentType(t, rec)
	if rec.Body.String() != `{"status":"ok"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestListIdeas_Happy(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertContentType(t, rec)

	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	// idea1, idea2 + kendi ai_blended kartı (idea3, testSID'e ait) = 3.
	if len(body.Ideas) != 3 {
		t.Fatalf("3 idea beklenirdi, geldi: %d", len(body.Ideas))
	}
}

func TestListIdeas_MissingSessionID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"bad_request"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestListIdeas_MalformedSessionID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	for _, sid := range []string{"not-hex!!", "ab", strings.Repeat("a", 3)} {
		rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas", sid, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("sid=%q: status %d bekleniyordu 400", sid, rec.Code)
		}
	}
}

func TestListIdeas_ExcludesOthersAIBlended(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas", testSID2, nil)
	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, i := range body.Ideas {
		if i.SourceType == "ai_blended" {
			t.Errorf("başkasının ai_blended kartı listede: %+v", i)
		}
	}
	if len(body.Ideas) != 2 {
		t.Errorf("2 idea beklenirdi (ai_blended hariç), geldi: %d", len(body.Ideas))
	}
}

func TestListIdeas_SourceTypeFilter(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=pain_point")
	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Ideas) != 1 || body.Ideas[0].SourceType != "pain_point" {
		t.Errorf("filtre çalışmadı: %+v", body.Ideas)
	}
}

func TestListIdeas_UnknownSourceType(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=bilinmeyen")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"ideas":[]}`+"\n" {
		t.Errorf("bilinmeyen source_type boş liste dönmeli, geldi: %s", rec.Body.String())
	}
}

func TestListIdeas_EmptyResult_NotNull(t *testing.T) {
	fs := newFakeStore()
	fs.ideas = nil // nil slice tuzağı
	s := newTestServer(fs, &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=pain_point")
	if strings.Contains(rec.Body.String(), "null") {
		t.Errorf("nil slice null'a sızmış: %s", rec.Body.String())
	}
	if rec.Body.String() != `{"ideas":[]}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestListIdeas_LimitBounds(t *testing.T) {
	fs := newFakeStore()
	s := newTestServer(fs, &fakeLLM{})

	doReq(t, s.Handler(), "/api/ideas?limit=0")
	if fs.lastFilter.Limit != 0 {
		t.Errorf("limit=0 filtreye 0 gitmeli (default kararı store'a bırakılır), geldi: %d", fs.lastFilter.Limit)
	}

	doReq(t, s.Handler(), "/api/ideas?limit=abc")
	if fs.lastFilter.Limit != 0 {
		t.Errorf("geçersiz limit savunmacı 0'a düşmeli, geldi: %d", fs.lastFilter.Limit)
	}

	doReq(t, s.Handler(), "/api/ideas?limit=500")
	if fs.lastFilter.Limit != 500 {
		t.Errorf("ham limit filtreye geçmeli (üst sınır store'da uygulanır), geldi: %d", fs.lastFilter.Limit)
	}
}

func TestGetIdea_Happy(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Idea store.Idea `json:"idea"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Idea.ID != 1 {
		t.Errorf("id: %d", body.Idea.ID)
	}
}

func TestGetIdea_NotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"not_found"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestGetIdea_InvalidID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	for _, id := range []string{"abc", "-1", "0", "1.5"} {
		rec := doReq(t, s.Handler(), "/api/ideas/"+id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id=%q: status %d bekleniyordu 404", id, rec.Code)
		}
	}
}

func TestGetIdea_MissingSessionID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas/1", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestGetIdea_OthersAIBlended_NotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	// idea3 testSID'e ait; testSID2 ile isteyince 404 (var olduğu sızmaz).
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas/3", testSID2, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d (404 bekleniyordu)", rec.Code)
	}
}

func TestGetIdea_OwnAIBlended_Visible(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas/3", testSID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Idea store.Idea `json:"idea"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Idea.Mine {
		t.Errorf("mine=true bekleniyordu")
	}
}

func TestIdeaSources_Happy(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/1/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sources []store.IdeaSource `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("1 kaynak beklenirdi, geldi: %d", len(body.Sources))
	}
}

func TestIdeaSources_EmptyNotNull(t *testing.T) {
	// idea 2 var ama sources map'inde kaydı yok -> fakeStore nil döner.
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/2/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "null") {
		t.Errorf("nil slice null'a sızmış: %s", rec.Body.String())
	}
	if rec.Body.String() != `{"sources":[]}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestIdeaSources_CardNotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/999/sources")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestIdeaSources_InvalidID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/abc/sources")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestNotFoundPath(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/bilinmeyen-yol")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"not_found"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------- kart sohbeti

func TestGetChat_EmptyIsEmptyArrayNotNull(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/1/chat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"messages":[]}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestGetChat_MissingSessionID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReqSID(t, s.Handler(), http.MethodGet, "/api/ideas/1/chat", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestGetChat_CardNotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/999/chat")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostChat_Happy(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	body, _ := json.Marshal(map[string]string{"message": "Bu fikri nasıl büyütebilirim?", "lang": "tr"})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Reply       store.ChatMessage `json:"reply"`
		Suggestions []string          `json:"suggestions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Reply.Role != "assistant" || resp.Reply.Message == "" {
		t.Errorf("reply: %+v", resp.Reply)
	}
	if len(resp.Suggestions) != 2 {
		t.Errorf("suggestions: %v", resp.Suggestions)
	}

	// GET /chat artık geçmişi göstermeli (kullanıcı + asistan mesajı).
	getRec := doReq(t, s.Handler(), "/api/ideas/1/chat")
	var getBody struct {
		Messages []store.ChatMessage `json:"messages"`
	}
	_ = json.Unmarshal(getRec.Body.Bytes(), &getBody)
	if len(getBody.Messages) != 2 {
		t.Fatalf("2 mesaj beklenirdi (user+assistant), geldi: %d", len(getBody.Messages))
	}
	if getBody.Messages[0].Role != "user" || getBody.Messages[1].Role != "assistant" {
		t.Errorf("mesaj sırası yanlış: %+v", getBody.Messages)
	}
}

func TestPostChat_EmptyMessage_BadRequest(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	body, _ := json.Marshal(map[string]string{"message": "   ", "lang": "tr"})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostChat_TooLongMessage_BadRequest(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	body, _ := json.Marshal(map[string]string{"message": strings.Repeat("a", 1001), "lang": "tr"})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostChat_MalformedJSON_BadRequest(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, []byte(`{bozuk`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostChat_CardNotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	body, _ := json.Marshal(map[string]string{"message": "merhaba", "lang": "tr"})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/999/chat", testSID, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostChat_UpstreamError(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{err: context.DeadlineExceeded})
	body, _ := json.Marshal(map[string]string{"message": "merhaba", "lang": "tr"})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"error":"upstream"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestPostChat_RateLimited(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeChatOK})
	body, _ := json.Marshal(map[string]string{"message": "merhaba", "lang": "tr"})

	var last *httptest.ResponseRecorder
	for i := 0; i < chatRateLimit+1; i++ {
		last = doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID, body)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("%d. istek status: %d (429 bekleniyordu)", chatRateLimit+1, last.Code)
	}
	if last.Body.String() != `{"error":"rate_limited"}`+"\n" {
		t.Errorf("gövde: %s", last.Body.String())
	}

	// Farklı bir oturum aynı anda kotasını tüketmemiş olmalı.
	freshRec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/chat", testSID2, body)
	if freshRec.Code != http.StatusOK {
		t.Errorf("farklı oturum kotası paylaşmamalı, status: %d", freshRec.Code)
	}
}

// ---------------------------------------------------------------- blend

func TestPostBlend_NoConversation_Conflict(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeBlendOK})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", testSID, []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"error":"no_conversation"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestPostBlend_Happy(t *testing.T) {
	fs := newFakeStore()
	fs.chat = map[int64]map[string][]store.ChatMessage{
		1: {testSID: {{ID: 1, Role: "user", Message: "merhaba", CreatedAt: time.Now()}}},
	}
	s := newTestServer(fs, &fakeLLM{response: fakeBlendOK})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", testSID, []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Idea store.Idea `json:"idea"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Idea.SourceType != "ai_blended" {
		t.Errorf("source_type: %q", body.Idea.SourceType)
	}
	if body.Idea.ParentIdeaID == nil || *body.Idea.ParentIdeaID != 1 {
		t.Errorf("parent_idea_id: %v", body.Idea.ParentIdeaID)
	}
	if !body.Idea.Mine {
		t.Errorf("mine=true bekleniyordu")
	}
}

func TestPostBlend_UpstreamError(t *testing.T) {
	fs := newFakeStore()
	fs.chat = map[int64]map[string][]store.ChatMessage{
		1: {testSID: {{ID: 1, Role: "user", Message: "merhaba", CreatedAt: time.Now()}}},
	}
	s := newTestServer(fs, &fakeLLM{err: context.DeadlineExceeded})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", testSID, []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestPostBlend_InvalidDraft_Upstream(t *testing.T) {
	fs := newFakeStore()
	fs.chat = map[int64]map[string][]store.ChatMessage{
		1: {testSID: {{ID: 1, Role: "user", Message: "merhaba", CreatedAt: time.Now()}}},
	}
	// title çok kısa -> copilot.Blend ErrInvalidDraft döner -> 502, kart yazılmaz.
	s := newTestServer(fs, &fakeLLM{response: `{"title":"kısa","problem_statement":"x","proposed_solution":"y","domain_tags":["a"]}`})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", testSID, []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fs.ideas) != 3 {
		t.Errorf("geçersiz taslakta kart YAZILMAMALI, idea sayısı: %d", len(fs.ideas))
	}
}

func TestPostBlend_RateLimited(t *testing.T) {
	fs := newFakeStore()
	fs.chat = map[int64]map[string][]store.ChatMessage{
		1: {testSID: {{ID: 1, Role: "user", Message: "merhaba", CreatedAt: time.Now()}}},
	}
	s := newTestServer(fs, &fakeLLM{response: fakeBlendOK})

	var last *httptest.ResponseRecorder
	for i := 0; i < blendRateLimit+1; i++ {
		last = doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", testSID, []byte(`{"lang":"tr"}`))
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("%d. istek status: %d (429 bekleniyordu)", blendRateLimit+1, last.Code)
	}
}

func TestPostBlend_MissingSessionID(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeBlendOK})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/1/blend", "", []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestPostBlend_CardNotFound(t *testing.T) {
	s := newTestServer(newFakeStore(), &fakeLLM{response: fakeBlendOK})
	rec := doReqSID(t, s.Handler(), http.MethodPost, "/api/ideas/999/blend", testSID, []byte(`{"lang":"tr"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	got := rec.Header().Get("Content-Type")
	if got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: %q", got)
	}
}

// TestTimeout_RealServer, httptest.ResponseRecorder DEĞİL gerçek bir
// sunucu (httptest.NewServer) üzerinden zaman aşımını doğrular: stdlib'in
// http.TimeoutHandler'ı 503 + text/plain döner, sözleşme ise tüm yanıtların
// JSON olmasını ister. Yavaş handler'ı simülemek için fakeStore.delay,
// sunucunun zaman aşımından uzun tutulur.
func TestTimeout_RealServer(t *testing.T) {
	fs := newFakeStore()
	fs.delay = 80 * time.Millisecond
	s := newServer(fs, &fakeLLM{}, 10*time.Millisecond)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/ideas", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Session-Id", testSID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (503 bekleniyordu)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("gövde JSON değil: %v", err)
	}
	if body["error"] != "timeout" {
		t.Errorf(`gövde {"error":"timeout"} olmalıydı, geldi: %v`, body)
	}

	// fakeStore'un arkaplan goroutine'i gecikmesini tamamlayıp yazmaya
	// çalıştığında timeoutWriter bunu sessizce yutmalı (ikinci yanıt
	// gitmemeli) — test sürecinin goroutine'i temiz bitirmesine izin ver.
	time.Sleep(fs.delay + 40*time.Millisecond)
}
