package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The fixed texts of the approval pages.
const (
	textApprovalNotFound = "This approval was not found."
	textNotWaiting       = "This action is no longer waiting for approval: it was approved, rejected, or it expired."
	textAnswerInChat     = "This action waits for an answer in the chat where the agent asked. It cannot be approved or rejected here."
	textSent             = "The mail server accepted the message."
	textTrashed          = "Moved to Trash"
	textRejected         = "Nothing was done."
	textNotSent          = "Not sent"
)

// plainCompose is a short new message.
func plainCompose() mail.Compose {
	return mail.Compose{To: []string{"bob@example.com"}, Subject: "Lunch", Body: "See you at noon."}
}

func approvalPath(id string) string { return "/approvals/" + id }

func TestApprovalsListShowsOnlyWhatWaitsHere(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDelete })
	send := p.fallbackSend(p.tenant, a, plainCompose())
	inBand := p.inBandSend(a, plainCompose())
	_, _, err := p.svc.Trash(agent(p.tenant), a.ID, []mail.Ref{"r1", "r2"}, service.ConfirmFallback, nil)
	require.NoError(t, err)

	resp, body := p.get("/approvals")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `href="/approvals/`+send+`"`)
	require.NotContains(t, body, inBand, "an in-band action is answered in the chat")
	require.Equal(t, 2, queueRows(t, body))
	text := visibleText(t, body)
	require.Contains(t, text, "Lunch")
	require.Contains(t, text, "bob@example.com")
	require.Contains(t, text, "Move 2 messages to Trash")

	resp, body = p.get("/approvals")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, queueRows(t, body), "showing the list changes nothing")
	require.Empty(t, p.sender.sent())
}

// The page shows the whole message: every envelope recipient (the Bcc too),
// the subject, every attachment and the whole text, as text.
func TestApprovalPageShowsTheWholeMessage(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	long := strings.Repeat("Some more words of the message. ", 40) + "LAST-WORDS"
	c := mail.Compose{
		To:      []string{"bob@example.com"},
		Bcc:     []string{"hidden@example.com"},
		Subject: "Figures <b>now</b>",
		Body:    "Hi Bob, <script>alert(1)</script>\n" + long,
		Attachments: []mail.OutgoingAttachment{
			{Filename: "figures.xlsx", ContentType: "application/octet-stream", Data: []byte("1,2")},
			{Filename: "photo.png", ContentType: "image/png", Data: []byte("png")},
		},
	}
	id := p.fallbackSend(p.tenant, a, c)

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	for _, want := range []string{
		"me@example.com", "bob@example.com", "hidden@example.com", "Figures <b>now</b>",
		"figures.xlsx", "photo.png", "Hi Bob, <script>alert(1)</script>", "LAST-WORDS",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, body, "<script>alert(1)</script>", "the text is escaped")
	require.Contains(t, body, "&lt;script&gt;")
	requireNoMarkup(t, body)
	requireButtons(t, body, id)
}

// requireButtons checks that the page has the Approve and Reject forms of the
// action, each a POST with the CSRF token.
func requireButtons(t *testing.T, page, id string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	actions := map[string]bool{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Form {
			require.Equal(t, "post", attr(n, "method"))
			actions[attr(n, "action")] = strings.Contains(renderNode(t, n), `name="csrf_token"`)
		}
	}
	require.Equal(t, map[string]bool{
		"/approvals/" + id + "/approve": true,
		"/approvals/" + id + "/reject":  true,
	}, actions)
}

func renderNode(t *testing.T, n *html.Node) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, html.Render(&b, n))
	return b.String()
}

// requireNoMarkup checks that the page has no element that a message could
// bring: no script, image, link out, frame, style or object.
func requireNoMarkup(t *testing.T, page string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		switch n.DataAtom {
		case atom.Script:
			// The only script is the file of the binary, with nothing inside it.
			require.True(t, strings.HasSuffix(attr(n, "src"), "/app.js"), "a script that is not the script of the binary")
			require.Nil(t, n.FirstChild, "a script with text")
		case atom.Img, atom.Iframe, atom.Style, atom.Object, atom.Embed, atom.B:
			t.Fatalf("the page has a <%s> element", n.Data)
		case atom.A:
			require.True(t, strings.HasPrefix(attr(n, "href"), "/"), "a link leaves the page: %s", attr(n, "href"))
		}
	}
}

// The HTML of a message is never rendered as HTML. The page shows the text
// that the recipient sees.
func TestApprovalPageNeverRendersTheHTMLOfAMessage(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	c := mail.Compose{
		To:      []string{"bob@example.com"},
		Subject: "News",
		HTMLBody: `<p>Read <b>BOLDWORD</b> now.</p><a href="https://evil.example/x">click</a>` +
			`<img src="https://tracker.example/p.png"><script>alert(2)</script><style>p{}</style>`,
	}
	id := p.fallbackSend(p.tenant, a, c)

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "BOLDWORD")
	require.NotContains(t, body, "<b>BOLDWORD")
	require.NotContains(t, body, `href="https://evil.example`)
	require.NotContains(t, body, "tracker.example")
	requireNoMarkup(t, body)
}

func TestApproveSendsQueuedMessage(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())
	require.Empty(t, p.sender.sent(), "nothing goes before the approval")

	resp, _ := p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, approvalPath(id), resp.Header.Get("Location"))

	sent := p.sender.sent()
	require.Len(t, sent, 1)
	require.Equal(t, []string{"bob@example.com"}, sent[0].Recipients)
	require.Equal(t, confirm.StateDone, p.pending(id).State)
	last := p.entries()[0]
	require.Equal(t, "send_message", last.Tool)
	require.Equal(t, "approved_on_web", last.Detail)
	require.Equal(t, "web", last.Client)

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textSent)
	require.NotContains(t, body, "/approve", "a done action has no buttons")

	// A second approval sends nothing.
	resp, body = p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textNotWaiting)
	require.Len(t, p.sender.sent(), 1)
}

func TestRejectMarksDeclined(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())

	resp, _ := p.post(approvalPath(id)+"/reject", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, approvalPath(id), resp.Header.Get("Location"))

	require.Equal(t, confirm.StateRejected, p.pending(id).State)
	last := p.entries()[0]
	require.Equal(t, "send_message", last.Tool)
	require.Equal(t, audit.OutcomeDeclined, last.Outcome)
	require.Equal(t, "rejected_on_web", last.Detail)
	got, err := p.svc.SendStatus(agent(p.tenant), a.ID, id)
	require.NoError(t, err)
	require.Equal(t, service.SendDeclined, got.Status)

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textRejected)
	resp, _ = p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Empty(t, p.sender.sent())
}

func TestExpiredApprovalCannotBeApproved(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())
	p.clock.Set(t0.Add(confirm.ApprovalLifetime + time.Millisecond))

	resp, body := p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textNotWaiting)
	require.Empty(t, p.sender.sent())
	require.Equal(t, confirm.StateExpired, p.pending(id).State)

	resp, body = p.get("/approvals")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Zero(t, queueRows(t, body), "an expired action is not listed")
	resp, body = p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "It can no longer be approved.")
	require.NotContains(t, body, "/approve")
}

// An action that waits for the answer in the chat is not on the web page, and
// the page cannot approve or reject it.
func TestInBandActionIsNotOnTheApprovalsPage(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.inBandSend(a, plainCompose())

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textApprovalNotFound)
	require.NotContains(t, body, "bob@example.com")
	for _, action := range []string{"/approve", "/reject"} {
		resp, body = p.post(approvalPath(id)+action, nil)
		require.Equal(t, http.StatusConflict, resp.StatusCode, action)
		require.Contains(t, visibleText(t, body), textAnswerInChat, action)
	}
	require.Equal(t, confirm.StatePending, p.pending(id).State)
	require.Empty(t, p.sender.sent())
}

func TestApprovalOfAnotherTenantIsNotFound(t *testing.T) {
	p := newPageFixture(t)
	p.addTenant("other")
	theirs := p.addAccount("other", "them@example.com")
	id := p.fallbackSend("other", theirs, plainCompose())

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NotContains(t, body, "bob@example.com")
	for _, action := range []string{"/approve", "/reject"} {
		resp, _ = p.post(approvalPath(id)+action, nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, action)
	}
	got, _, err := p.confirm.Get(context.Background(), "other", id)
	require.NoError(t, err)
	require.Equal(t, confirm.StatePending, got.State)
	require.Empty(t, p.sender.sent())
	resp, _ = p.post("/approvals/no-such-action/approve", url.Values{})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// textCannotShow is the note of the approval page when the whole message
// cannot be built now.
const textCannotShow = "The whole message cannot be shown now, so it cannot be approved."

// requireOnlyReject checks that the page has the Reject form and no Approve
// form.
func requireOnlyReject(t *testing.T, page, id string) {
	t.Helper()
	require.Contains(t, page, `action="/approvals/`+id+`/reject"`)
	require.NotContains(t, page, `action="/approvals/`+id+`/approve"`)
}

// When the whole message cannot be built (the mail server does not answer for
// a reply), the page shows the stored start and a note, and offers only
// Reject. Approve is refused on the server too. When the server answers again,
// the page shows all of it and offers Approve.
func TestApprovalPageWithoutTheWholeMessageOffersOnlyReject(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: strings.Repeat("w", 600) + " THE-END"}
	id := p.fallbackSend(p.tenant, a, c)
	p.backends.backend.setGetErr(fmt.Errorf("imap: read: %w", imapsmtp.ErrConnectionLost))

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, textCannotShow)
	require.Contains(t, text, "alice@example.com")
	require.NotContains(t, text, "THE-END", "only the stored start is shown")
	require.Contains(t, text, "… and 108 more characters not shown", "the page says that the text goes on")
	requireOnlyReject(t, body, id)

	resp, body = p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "The mail server could not do this.")
	requireOnlyReject(t, body, id)
	require.Empty(t, p.sender.sent())
	require.Equal(t, confirm.StatePending, p.pending(id).State)

	p.backends.backend.setGetErr(nil)
	resp, body = p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "THE-END")
	requireButtons(t, body, id)
	resp, _ = p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Len(t, p.sender.sent(), 1)
	require.Contains(t, string(p.sender.sent()[0].Data), "THE-END")
}

// trashApproval asks to move messages to Trash for the web page, and returns
// the id of the action.
func (p *pageFixture) trashApproval(a *account.Account, refs ...mail.Ref) string {
	p.t.Helper()
	res, _, err := p.svc.Trash(agent(p.tenant), a.ID, refs, service.ConfirmFallback, nil)
	require.NoError(p.t, err)
	require.Equal(p.t, service.TrashPendingApproval, res.Status)
	list, err := p.svc.PendingApprovals(service.WithPrincipal(context.Background(), service.Principal{Tenant: p.tenant}))
	require.NoError(p.t, err)
	for _, pending := range list {
		if pending.Kind == confirm.KindTrash {
			return pending.ID
		}
	}
	p.t.Fatal("the Trash action is not waiting")
	return ""
}

// A Trash action of more than 20 messages lists 20, and says how many more
// there are.
func TestTrashApprovalPageSaysHowManyAreNotListed(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDelete })
	refs := make([]mail.Ref, 23)
	for i := range refs {
		refs[i] = mail.Ref(fmt.Sprintf("r%02d", i))
	}

	id := p.trashApproval(a, refs...)
	_, body := p.get(approvalPath(id))

	require.Equal(t, 20, strings.Count(body, "<li>(no date) — alice@example.com — Question</li>"))
	require.Contains(t, visibleText(t, body), "… and 3 more not shown")
}

func TestTrashApprovalPage(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDelete })

	id := p.trashApproval(a, "r1", "r2")
	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "Move 2 messages of me@example.com to Trash")
	// The page lists the messages, so the person sees which ones go to Trash.
	require.Equal(t, 2, strings.Count(body, "<li>(no date) — alice@example.com — Question</li>"), body)
	requireButtons(t, body, id)
	require.Empty(t, p.backends.backend.trashCalls(), "showing the page moves nothing")

	resp, _ = p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, [][]mail.Ref{{"r1", "r2"}}, p.backends.backend.trashCalls())
	require.Equal(t, confirm.StateDone, p.pending(id).State)
	last := p.entries()[0]
	require.Equal(t, "trash_messages", last.Tool)
	require.Equal(t, "approved_on_web", last.Detail)
	require.Equal(t, a.ID, last.Account)
	_, body = p.get(approvalPath(id))
	require.Contains(t, visibleText(t, body), textTrashed)
	require.NotContains(t, body, "/approve")

	other := p.trashApproval(a, "r3")
	resp, _ = p.post(approvalPath(other)+"/reject", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, confirm.StateRejected, p.pending(other).State)
	last = p.entries()[0]
	require.Equal(t, "trash_messages", last.Tool)
	require.Equal(t, audit.OutcomeDeclined, last.Outcome)
	require.Equal(t, "rejected_on_web", last.Detail)
	require.Len(t, p.backends.backend.trashCalls(), 1, "a rejected action moves nothing")
	_, body = p.get(approvalPath(other))
	require.Contains(t, visibleText(t, body), textRejected)
}

// The first action opens beside the list, so the user can answer it at once.
func TestApprovalsPageOpensTheFirstAction(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())

	resp, body := p.get("/approvals")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "See you at noon.")
	requireButtons(t, body, id)
	require.Contains(t, body, `aria-current="true"`)
	require.Empty(t, p.sender.sent())
}

// A page says so when nothing waits, and it has no buttons.
func TestApprovalsPageWithNothingToAnswer(t *testing.T) {
	p := newPageFixture(t)
	resp, body := p.get("/approvals")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "Nothing waits for you")
	require.NotContains(t, body, "<form")
}

// The Approve button of the page shows at once that it works.
func TestApprovalButtonsShowThatTheyWork(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())

	_, body := p.get(approvalPath(id))
	require.Contains(t, body, `data-busy="Sending…">Approve and send</button>`)
	require.Contains(t, body, `data-busy="Rejecting…">Reject</button>`)
	require.Contains(t, body, "data-busy-group")
}

// The menu counts the actions that wait, on every page.
func TestMenuCountsWhatWaits(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	_, body := p.get("/accounts")
	require.NotContains(t, body, `class="count"`)

	id := p.fallbackSend(p.tenant, a, plainCompose())
	_, body = p.get("/accounts")
	require.Contains(t, body, `<span class="count">1</span>`)

	p.post(approvalPath(id)+"/reject", nil)
	_, body = p.get("/accounts")
	require.NotContains(t, body, `class="count"`)
}

// A send that the mail server refuses for good never looks like a success, even
// though the service has finished the action.
func TestFailedSendIsShownAsNotSent(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())
	p.sender.setErr(outbox.Permanent(errors.New("550 refused")))

	resp, _ := p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, confirm.StateDone, p.pending(id).State)

	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, textNotSent)
	require.NotContains(t, text, textSent)
	require.Contains(t, body, `class="outcome bad"`)
	require.NotContains(t, body, "550 refused", "the text of the mail server is not shown")
}

// A send that waits for its undo window is not called sent.
func TestQueuedSendSaysWhenItGoesOut(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.Limits.UndoWindowConfirmed = time.Minute })
	id := p.fallbackSend(p.tenant, a, plainCompose())

	resp, _ := p.post(approvalPath(id)+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Empty(t, p.sender.sent())

	_, body := p.get(approvalPath(id))
	text := visibleText(t, body)
	require.Contains(t, text, "It goes out at "+t0.Add(time.Minute).Local().Format("15:04:05")+".")
	require.NotContains(t, text, textSent)
}

// The result says what was answered, and leads to the next action.
func TestResultShowsTheActionAndTheNextOne(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	next := p.fallbackSend(p.tenant, a, mail.Compose{To: []string{"carol@example.com"}, Subject: "Later", Body: "Hi."})
	id := p.fallbackSend(p.tenant, a, plainCompose())

	p.post(approvalPath(id)+"/approve", nil)
	_, body := p.get(approvalPath(id))
	outcome := element(t, strings.ReplaceAll(body, `class="outcome good"`, `id="result"`), atom.Section, "result")
	require.NotNil(t, outcome)
	text := textOf(outcome)
	require.Contains(t, text, "Lunch")
	require.Contains(t, text, "To bob@example.com")
	require.Contains(t, renderNode(t, outcome), `href="/approvals/`+next+`"`)
	require.Contains(t, text, "Next: Later")
}

// A note lives for a short time, and their number has a limit.
func TestAnswerNotesAreShortLived(t *testing.T) {
	notes := answerNotes{items: make(map[string]answerNote)}
	notes.put("a", answerNote{What: "Lunch", at: t0})
	require.Equal(t, "Lunch", notes.get("a", t0.Add(answerNoteTTL-time.Second)).What)
	require.Nil(t, notes.get("a", t0.Add(answerNoteTTL)))
	require.Nil(t, notes.get("b", t0))

	notes.put("b", answerNote{at: t0.Add(answerNoteTTL)})
	require.NotContains(t, notes.items, "a", "a new note drops the old notes")

	for i := range 2 * maxAnswerNotes {
		notes.put(fmt.Sprint(i), answerNote{at: t0.Add(answerNoteTTL)})
	}
	require.LessOrEqual(t, len(notes.items), maxAnswerNotes)
}

func TestTimeLeft(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:              "Under 1 min left",
		5 * time.Minute:               "5 min left",
		23*time.Hour + 59*time.Minute: "23 h left",
	} {
		require.Equal(t, want, timeLeft(d))
	}
}

// The list names one recipient, and counts many. The page of the action shows
// every one.
func TestListCountsManyRecipients(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, mail.Compose{
		To: []string{"bob@example.com", "carol@example.com"}, Subject: "Lunch", Body: "Hi.",
	})

	_, body := p.get(approvalPath(id))
	require.Contains(t, visibleText(t, body), "To 2 people")
	require.Contains(t, visibleText(t, body), "carol@example.com")
}

// On a failed send only the mark and the title are loud: the way on is quiet.
func TestFailedSendHasAQuietNextButton(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.fallbackSend(p.tenant, a, plainCompose())
	next := p.fallbackSend(p.tenant, a, plainCompose())
	p.sender.setErr(outbox.Permanent(errors.New("550 refused")))

	p.post(approvalPath(id)+"/approve", nil)
	_, body := p.get(approvalPath(id))
	require.Contains(t, body, `<a class="button" href="/approvals/`+next+`">`)
}

// The title of the tab counts what waits, as in a mail app.
func TestTabTitleCountsWhatWaits(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	_, body := p.get("/accounts")
	require.Contains(t, body, "<title>Accounts · email-mcp</title>")

	p.fallbackSend(p.tenant, a, plainCompose())
	_, body = p.get("/accounts")
	require.Contains(t, body, "<title>(1) Accounts · email-mcp</title>")
}

func TestAnswerErrorTextMailServerConnection(t *testing.T) {
	for name, err := range map[string]error{
		"the server cannot be reached": fmt.Errorf("imap: connect: %w", imapsmtp.ErrServerUnreachable),
		"the connection was lost":      fmt.Errorf("imap: read: %w", imapsmtp.ErrConnectionLost),
	} {
		t.Run(name, func(t *testing.T) {
			status, text, known := answerErrorText(err)

			require.True(t, known)
			require.Equal(t, http.StatusBadGateway, status)
			require.Equal(t, msgMailServer, text)
		})
	}

	t.Run("a file error is not about the mail server", func(t *testing.T) {
		_, _, known := answerErrorText(&fs.PathError{Op: "open", Path: "/some/file", Err: syscall.ENOENT})

		require.False(t, known)
	})
}
