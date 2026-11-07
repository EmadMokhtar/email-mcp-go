---
name: email-mcp portal
description: A small mail app with one folder, the actions an agent wants, held until the person decides.
colors:
  ground: "#f4f6f3"
  pane: "#ffffff"
  hover: "#edf1ed"
  ink: "#16201c"
  soft: "#55615b"
  line: "#dde2de"
  line-strong: "#8a958f"
  go: "#0b6b4f"
  go-hover: "#085a42"
  go-ink: "#ffffff"
  go-text: "#0b6b4f"
  go-soft: "#e2eee8"
  warn-soft: "#fbf1d9"
  warn-ink: "#5c4300"
  warn-line: "#e6d29d"
  bad: "#b3261e"
  bad-hover: "#93201a"
  bad-ink: "#ffffff"
  bad-text: "#a4231c"
  bad-soft: "#fbe9e7"
  bad-line: "#f0c6c2"
  info-soft: "#e9eef0"
  ground-dark: "#131414"
  pane-dark: "#1a1b1b"
  hover-dark: "#252626"
  ink-dark: "#e9eaea"
  soft-dark: "#a9adab"
  line-dark: "#2e3030"
  line-strong-dark: "#6f7371"
  go-dark: "#39b98f"
  go-hover-dark: "#4fcaa1"
  go-ink-dark: "#06140f"
  go-text-dark: "#5fd0aa"
  go-soft-dark: "#17322a"
  warn-soft-dark: "#2e2613"
  warn-ink-dark: "#eed594"
  warn-line-dark: "#54462a"
  bad-dark: "#e5675e"
  bad-hover-dark: "#ee8078"
  bad-ink-dark: "#1c0705"
  bad-text-dark: "#f19a93"
  bad-soft-dark: "#351b19"
  bad-line-dark: "#5a2f2b"
  info-soft-dark: "#262727"
typography:
  headline:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "1.625rem"
    fontWeight: 650
    lineHeight: 1.25
    letterSpacing: "-0.02em"
  empty-title:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "1.375rem"
    fontWeight: 650
    lineHeight: 1.25
  title:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  row-title:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  body:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.55
  body-small:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "0.9375rem"
    fontWeight: 400
    lineHeight: 1.55
  control:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "0.9375rem"
    fontWeight: 500
    lineHeight: 1.25
  control-strong:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "0.9375rem"
    fontWeight: 600
    lineHeight: 1.25
  label:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.55
  caption:
    fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.55
  code:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "0.875em"
    fontWeight: 400
    lineHeight: 1.5
rounded:
  sm: "4px"
  md: "8px"
  lg: "12px"
  full: "999px"
spacing:
  step-2: "2px"
  step-4: "4px"
  step-6: "6px"
  step-8: "8px"
  step-10: "10px"
  step-12: "12px"
  step-14: "14px"
  step-16: "16px"
  step-20: "20px"
  step-24: "24px"
  gutter: "44px"
components:
  button:
    backgroundColor: "{colors.pane}"
    textColor: "{colors.ink}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "8px 16px"
    height: "40px"
  button-hover:
    backgroundColor: "{colors.hover}"
    textColor: "{colors.ink}"
  button-primary:
    backgroundColor: "{colors.go}"
    textColor: "{colors.go-ink}"
    typography: "{typography.control-strong}"
    rounded: "{rounded.md}"
    padding: "8px 16px"
    height: "40px"
  button-primary-hover:
    backgroundColor: "{colors.go-hover}"
    textColor: "{colors.go-ink}"
  button-danger:
    backgroundColor: "{colors.bad}"
    textColor: "{colors.bad-ink}"
    typography: "{typography.control-strong}"
    rounded: "{rounded.md}"
    padding: "8px 16px"
    height: "40px"
  button-danger-hover:
    backgroundColor: "{colors.bad-hover}"
    textColor: "{colors.bad-ink}"
  button-danger-quiet:
    backgroundColor: "{colors.pane}"
    textColor: "{colors.bad-text}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "4px 12px"
    height: "34px"
  input:
    backgroundColor: "{colors.pane}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "7px 10px"
    height: "40px"
  menu-link:
    textColor: "{colors.soft}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 10px"
  menu-link-hover:
    backgroundColor: "{colors.hover}"
    textColor: "{colors.ink}"
  menu-link-current:
    backgroundColor: "{colors.go-soft}"
    textColor: "{colors.go-text}"
  count:
    backgroundColor: "{colors.go}"
    textColor: "{colors.go-ink}"
    typography: "{typography.caption}"
    rounded: "{rounded.full}"
  queue-item:
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "12px 20px"
  queue-item-hover:
    backgroundColor: "{colors.hover}"
  queue-item-current:
    backgroundColor: "{colors.go-soft}"
  action-bar:
    backgroundColor: "{colors.pane}"
    padding: "14px 0"
  notice:
    backgroundColor: "{colors.info-soft}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "12px 16px"
  notice-warn:
    backgroundColor: "{colors.warn-soft}"
    textColor: "{colors.warn-ink}"
    rounded: "{rounded.lg}"
    padding: "12px 16px"
  notice-bad:
    backgroundColor: "{colors.bad-soft}"
    textColor: "{colors.bad-text}"
    rounded: "{rounded.lg}"
    padding: "12px 16px"
  notice-good:
    backgroundColor: "{colors.go-soft}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "12px 16px"
  outcome-mark-good:
    backgroundColor: "{colors.go}"
    textColor: "{colors.go-ink}"
    rounded: "{rounded.full}"
    size: "44px"
  outcome-mark-bad:
    backgroundColor: "{colors.bad}"
    textColor: "{colors.bad-ink}"
    rounded: "{rounded.full}"
    size: "44px"
  outcome-mark-info:
    backgroundColor: "{colors.info-soft}"
    textColor: "{colors.ink}"
    rounded: "{rounded.full}"
    size: "44px"
---

# Design System: email-mcp portal

## Overview

**Creative North Star: "The Held Mail"**

The portal is a small mail app with one folder. That folder holds the actions the agent wants, and each one stays held until the person decides. The person sees what waits, reads the whole action, decides, sees at once that the click works, and then sees the true result. Every surface serves that sequence, and setup comes second.

The look is calm and dense in the way a mail client is dense: a slightly tinted ground, white reading panes, thin rules between things, and very little colour. Colour carries meaning here and never decoration. Green says where you are and what to do next. Red and amber appear for real risk. Everything else is ink on a pane.

The system rejects the admin panel: on a desktop screen there is no top bar, and no table or setup page comes first. It is also flat, with no shadow anywhere, and it is self-contained. All of it ships inside the binary, so what you see depends on nothing from another site.

**Key Characteristics:**
- Three panes on a wide screen: menu, waiting list, open action.
- The decision buttons stay at the top of the open action, and at the bottom of the screen on a phone.
- One green, and a closed list of places where it may appear.
- Thin rules separate content. Boxes are for notices.
- No shadows, no gradients, no uppercase labels.
- Light and dark follow the setting of the computer.

## Colors

A restrained palette: tinted neutrals, one green, and two risk colours that stay absent from a normal screen.

Each token also has a night value, under the same name with `-dark` added. These are the same token, chosen by the `prefers-color-scheme` setting of the computer, and not extra colours. At night the ground loses its green tint and becomes a plain neutral. Green and red become lighter, and the text on them becomes dark. Never pair a day value with a night value.

### Primary
- **Deep Go Green** (#0b6b4f): the fill of the primary button, the waiting count, and the mark of a good result. It is also the focus ring and the checkbox tick.
- **Darker Go Green** (#085a42): hover on the primary button, and nothing else.
- **Go Ink** (#ffffff): text and icons on a green fill.
- **Green Text** (#0b6b4f): links, the current menu item, and the status of a connected account. It has the same day value as Deep Go Green but a separate night value, which keeps text readable at night.
- **Green Wash** (#e2eee8): the background of the current place, the new-token notice, and text selection.

### Risk
- **Warning Wash, Warning Ink, Warning Edge** (#fbf1d9, #5c4300, #e6d29d): amber notices and the status of an account that needs attention.
- **Stop Red** (#b3261e): the fill of the one destructive button and the mark of a failed result.
- **Darker Stop Red** (#93201a): hover on the destructive button.
- **Stop Ink** (#ffffff): text and icons on a red fill.
- **Red Text** (#a4231c): failed result headings, the danger section heading, the error outcome in the activity log, and the quiet Revoke button.
- **Red Wash, Red Edge** (#fbe9e7, #f0c6c2): red notices.

### Neutral
- **Quiet Ground** (#f4f6f3): the page behind everything, seen as the menu column.
- **White Pane** (#ffffff): the page area, buttons, and inputs.
- **Pale Wash** (#edf1ed): hover on neutral things, and the background of inline code.
- **Green-Black Ink** (#16201c): all main text.
- **Soft Text** (#55615b): secondary text. It measures 5.95:1 on the ground and 6.47:1 on a pane by day, and 8.13:1 and 7.61:1 at night.
- **Hairline** (#dde2de): every rule between content.
- **Control Edge** (#8a958f): the border of buttons and inputs, the header rule of the table, and the scrollbar. It measures 3.1:1 on a pane by day and 3.59:1 at night.
- **Neutral Mark** (#e9eef0): the background of the neutral notice, and of the mark on a result that is neither good nor failed.

### Named Rules

**The One Green Rule.** Green marks the primary action and the current place. Other uses are a closed list: the waiting count that leads to the primary action, proof that something worked (the good result mark, the connected status, the new-token notice), and the standard controls of the page (links, focus ring, checkbox tick, text selection). Do not add to this list. Green is never decoration, and it is never a section tint.

**The Real Risk Rule.** Red and amber mark real risk only: a destructive action, a failed result or error, a missing part of a message, an unencrypted connection, a certificate problem, suggested server settings that need checking, a request to change a permission, an account that needs attention, or full autonomy. Only real risk looks alarming, so a normal screen has no red and no amber. An ordinary message uses the neutral notice.

**The Quiet Failure Rule.** A failed result has a neutral Next button, never a green one. The Next button is green only after a result that did not fail, because a failure must not push the person on. A failed result is never styled like a success.

## Typography

**Display Font:** none. The system has no display type, and its largest size is 1.625rem.
**Body Font:** the system interface face of the computer (system-ui, with Segoe UI, Roboto, Helvetica Neue, and Arial as fallbacks)
**Label/Mono Font:** the system monospace (ui-monospace, with SFMono-Regular, Menlo, and Consolas as fallbacks), for code, tokens, and fingerprints

**Character:** the type of the operating system, so the portal feels like a local tool. Hierarchy comes from weight and small size steps, with tight tracking on headings.

### Hierarchy
- **Headline** (650, 1.625rem, 1.25): the page heading, the subject of the open action, and the result heading.
- **Empty Title** (650, 1.375rem, 1.25): the heading of an empty state.
- **Title** (600, 1.125rem, 1.25): section headings, form group legends, and the heading above the waiting list.
- **Row Title** (600, 1rem, 1.25): the name in an account row or log row. Waiting list titles use the same size and weight at body line height.
- **Body** (400, 1rem, 1.55): reading text and input values.
- **Body Small** (400, 0.9375rem, 1.55): secondary sentences, label and value lists, and table cells.
- **Control** (500, 0.9375rem, 1.25): buttons. Primary and danger buttons use 600. Field labels use the same size and weight at body line height.
- **Label** (400, 0.875rem, 1.55): second lines in lists, and the time in the log. Table headers use 500.
- **Caption** (400, 0.8125rem, 1.55): the foot of a waiting item. The count uses 600 at the same size.
- **Code** (400, 0.875em, 1.5): tokens, fingerprints, and commands. The size is relative to the text around it.

Counts, times, and table values use tabular figures, so digits line up.

### Named Rules

**The Computer's Own Fonts Rule.** The portal uses system fonts only. There are no web fonts, no font files, and no CDN (content delivery network, a set of external file servers). Everything is inside the binary.

**The 34rem Rule.** Reading text is limited to 34rem wide, not a number of `ch` units, because `ch` is too wide in this font. Message bodies, sheet paragraphs, and the sentences of a result follow it. Headers and lists may run to 44rem, because their lines are short.

## Layout

The frame is a 224px menu beside the page. The menu sits on the ground and stays in view, and the page is a white pane. Sign out is the last item of the menu: a button that looks like a menu link, at the bottom of the column, and at the end of the row when the menu is a row. The mail view divides the page again: a waiting list of 280 to 340px, and a reader that takes the rest. The list and the reader each scroll alone, so the mail view fills exactly one screen height.

The reader has 44px side gutters. Its action bar stays at the top while the message scrolls. Message headers and results are limited to 44rem and start at the left, so there is free space on the right on a large screen. Other pages use a left-aligned sheet with 36px top padding and 44px side padding, 44rem wide for forms, 60rem wide for Accounts and Activity, and 66rem wide for Connections, whose token table has many columns. The form on Connections stays as wide as the other forms. Nothing is centred except empty states and the sign-in message.

Spacing is mostly on 4, 8, 12, 16, 20, and 24px. The steps of 10 and 14px are used for tighter fits inside lists and the menu.

Three breakpoints adapt the frame. At 1100px the label column of a value list narrows from 15rem to 9rem. At 900px the menu moves to a row at the top of the page. At 680px the list and the open action become separate steps: an open action fills the screen and hides the menu, a back link replaces it, and the action bar moves to the bottom of the screen with full-width buttons. Value lists stack and form grids become one column. The brand name is hidden, and the "for you" part of the menu label is visually hidden but stays readable by a screen reader.

### Named Rules

**The Decision Stays Reachable Rule.** While an action waits, its Reject and Approve buttons stay on screen at every length of message: fixed to the top of the reader, or to the bottom of a phone screen.

## Elevation & Depth

The system is flat. It has no box shadow, text shadow, gradient, or backdrop blur. Depth comes from two tones, the ground and the pane, and from hairlines. A sticky bar has an opaque pane background and a hairline, so content passes under it without a shadow.

### Named Rules

**The Flat Pane Rule.** Nothing floats. If a surface needs to stand apart, it gets a tone change or a hairline.

## Shapes

Corners are gently curved. Controls, menu items, and other interactive surfaces use 8px. Notices use 12px, inline code uses 4px, and counts use a full pill. Result marks and status dots are full circles. Panes and rows have no radius, because they meet the edge of their column.

Borders are 1px. Icons are inline line drawings on a 24 by 24 grid, drawn with a 2px round stroke in the colour of the text around them, and shown at 18px. The result mark uses a 24px icon with a 2.5px stroke.

### Named Rules

**The Rule, Not a Box Rule.** A group in a form or a page is set off by a hairline above it, never by a card. A box with a background means a notice, so a box always carries a message to the person.

## Components

Quiet, direct, and honest about state. A control looks ordinary until it is working, and then it says exactly what it is doing.

### Buttons
- **Shape:** gently curved (8px), at least 40px high, with a 1px border.
- **Neutral:** a white pane with ink text and the control edge. This is the default, and Reject uses it.
- **Primary:** green fill with white text at weight 600. It marks the action the person most likely wants next, so it can move: Reconnect becomes primary only when the account is not connected.
- **Danger:** red fill with white text, used only for the final Remove account button, after a confirmation checkbox.
- **Quiet Danger:** a neutral button with red text, 34px high, for a destructive action in a table row (Revoke).
- **Hover / Active:** the background changes in 150ms. A press scales to 0.98 in 100ms. Focus is a 2px green ring with a 2px offset.
- **Working:** see The Approval Bar below.

### Status
- **Style:** a small 8px dot in the text colour, followed by the words of the status. Green text means connected. Warning ink at weight 600 means attention is needed. The meaning is always in the words too.

### Notices
- **Corner Style:** softly curved (12px), with 1px borders and 12 by 16px padding. A notice that is a fieldset has 16px padding.
- **Neutral:** the neutral mark background, a hairline edge, and ink text. It is for ordinary messages with no risk, such as a page that was not found or server settings that were not found.
- **Warning and Red:** a wash background, a matching edge, and matching text.
- **Good:** a green wash with no visible edge and ink text. It is used for the one-time new token.
- **Behaviour:** a notice can hold a tick box, so the person accepts a risk inside the notice that explains it. Full autonomy, no encryption, and certificates work this way.

### Inputs / Fields
- **Style:** a white pane, the control edge, gently curved corners (8px), and at least 40px high. Labels sit above the field.
- **Hover:** the edge changes to Soft Text in 150ms.
- **Focus:** the edge turns green and the green ring sits against it, with no offset.
- **Checkboxes:** use the green accent, except inside a notice, where they take the colour of the notice text. The label wraps beside the box.
- **Layout:** paired fields use two columns and become one column on a phone.

### Navigation
- **Style:** a brand name, then body-size links with soft text, 8px curved corners, and 2px gaps.
- **Hover:** a pale wash and ink text.
- **Current:** a green wash, green text, and weight 600.
- **Waiting count:** a green pill at the right of the Waiting link, shown only when something waits. The page title repeats the number.
- **Mobile:** a row at the top, hidden while an action is open on a phone.

### The Waiting List
A column of full-width rows split by hairlines, with no cards. Each row has a bold title, an optional recipient line, and a foot with the account and the time left. Long text is cut with an ellipsis, on one line. The open row has a green wash. Hover uses the pale wash.

### Lists and Tables
Accounts and the activity log are rows split by hairlines, with a main column and an action or time at the right. Tables exist only for lists with many columns: the tokens and the signed-in browsers on the Connections page. A table has hairlines between rows and no fill, and it scrolls sideways alone. A header may wrap, and then sits on the rule below it. A time may wrap before the hour, but the date stays on one line.

### Empty States
A centred block of up to 30rem, with a heading, one soft sentence, and at most one button.

### The Approval Bar (signature)
The bar has a short note at the left and the decision buttons at the right. When a button is pressed, its text changes at once to what is happening ("Sending…"), and a 15px spinner appears. The other buttons lock at 0.55 opacity, and the pressed button keeps its width so the bar does not move. The note clears, and a screen reader hears the same text. After 10 seconds the note says that this is taking longer than usual. The buttons come back after 30 seconds, on Esc, or when the back button shows the page again, and the note then says to reload. This is safe, because the server carries out an action only once. The connection tests use the same note under their button.

### The Result (signature)
When the answer arrives, the pane becomes the result. A 44px circular mark comes in over 320ms, with a fade and a scale from 0.7. Next to it is a headline, red if the action failed. Below are what the action was, what happened, and the next action one click away. Keyboard focus moves to the headline, so a screen reader reads the result. This is the only entrance animation in the system. With reduced motion, all animation is nearly instant.

### Named Rules

**The True Result Rule.** Text claims only what is always true. A busy label says what is being attempted, and only the result says what happened. Safety wording stays exact: never rephrase, shorten, or restyle away the full autonomy, certificate, or no encryption warnings.

**The Plain Form Rule.** Every form works without app.js. The script only shows that a form is working, and it never reads or sends anything by itself. No inline script and no inline style in the pages: all style lives in the one stylesheet, and the Content Security Policy blocks the rest.

## Do's and Don'ts

### Do:
- **Do** put what waits for the person first, and setup second.
- **Do** show the whole action before asking for a decision, and show Approve only when the page shows the whole action.
- **Do** keep green to The One Green Rule's closed list, and use each token with its night value.
- **Do** limit reading text to 34rem, and do not measure it in `ch`.
- **Do** separate groups with a hairline, and keep boxes for notices.
- **Do** give a button a busy label when its action can take time: the approval decisions and the connection tests have one.
- **Do** make the next action one click away after a result, in a neutral button if the result failed.
- **Do** say a state in words as well as in colour.
- **Do** use the 150ms curve of `cubic-bezier(0.16, 1, 0.3, 1)` for state changes.

### Don't:
- **Don't** add a top bar above 900px, or open with a table or a setup page. Below 900px the menu row at the top is the correct layout, and a table remains correct for a list with many columns.
- **Don't** use red for Reject. Rejecting is an ordinary decision, so its button stays neutral.
- **Don't** use amber or red to add emphasis, and don't use green to decorate.
- **Don't** give a failed result a green button or a success mark.
- **Don't** write a sentence that is only sometimes true, and don't change the wording of a safety warning.
- **Don't** add web fonts, font files, a CDN, or anything from another site.
- **Don't** add inline script or inline style, or a form that needs app.js to work.
- **Don't** add shadows, gradients, or backdrop blur.
- **Don't** add uppercase labels, eyebrow text above headings, or icon characters from a font. Icons are inline line drawings.
- **Don't** wrap groups or list rows in cards.
