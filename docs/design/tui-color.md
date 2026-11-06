# TUI colour: a semantic theme (design)

_Drafted 2026-11-07 for CoS review, as the one design job CoS approved for Max's TUI colour taste work, item B (CoS msg_01M6RR2HJCBSTGHMY211DB9VSF on thr_01M6RR2HJCBSTGHMY20YZYYCCB). A separate integrator implements it later. Inputs: the web tokens in `internal/ui/static/base.css` `:root` (cited as [CSS]); the TUI at master `ace7bb8` and at PR #37 head `b92cc2a` (`internal/tui/*.go` and the goldens in `internal/tui/testdata`); the ui-shot harness (`scripts/ui/tmux.mjs`, `scripts/ui/ansi.mjs`); and the sources of lipgloss v2.0.6, bubbletea v2.1.1 and colorprofile v0.4.3. This PR changes docs only. It adds no Go code and no `internal/tui/theme` package._

## Summary

- **One theme, built from the web tokens.** Every colour the TUI draws comes from a named **role**: accent, unread, warn, success, error, info, muted, rules, selection, header bar and a few more. Screens ask for a role and never hold a hex value. Today about 30 hard-coded colour values live in four files, and several of them drift from the web (section 2).
- **Four rungs, picked by one rule.** The TUI uses truecolor when `COLORTERM` is `truecolor` or `24bit`. Otherwise it uses 256 colours, and below that the 16 basic colours. A non-empty `NO_COLOR` removes all colour and keeps the structure. Each role has a hand-picked value on every rung. The TUI does not rely on automatic nearest-colour downsampling (section 4).
- **The terminal's own background stays.** The TUI stops painting the whole screen. Today `View()` sets `#0b0f19` behind every cell and `#f3f4f6` as the text colour. Only selection rows and the header bar get a fill, and every filled cell sets its own foreground too, so it reads on any theme.
- **Dark and light terminals.** Each role has a dark value (the web token) and a light value (the same hue, deeper). The TUI asks the terminal for its background colour and picks one set. If the terminal gives no answer, the dark set is used. `VECNET_TUI_BG=light|dark` forces a set.
- **Unread is amber** (`--wake`), never red. Red (`--bad`) means an error and nothing else.
- **Colour is never the only signal.** The focused pane gets a heavy border. The selected row gets `>`, plus reverse video when there is no colour. Unread items get bold and their count. Sections get a `▌` bar.
- **Hostile text is never coloured by what it says.** A role comes from the field and from hub enums, and it wraps text that `Clean` has already sanitised.
- **Mock frames for every screen group** (section 8), each with a cell-exact paint mask. Inbox and chat also get `NO_COLOR` and 16-colour masks.
- **The integrator** stacks `internal/tui/theme` on PR #37 after CoS accepts this design. It must ship ui-shots of every TUI screen in the default theme, plus `NO_COLOR` and 16-colour shots of at least chat and inbox. CoS reviews the shots by eye before merging (section 10).

## Contents

1. Scope and sequencing
2. Where the TUI is today
3. Constraints
4. Terminal capability and background
5. Semantic roles
6. Composition rules
7. Structural changes the integrator makes
8. Mock frames, per screen group
9. Contrast evidence
10. Integrator plan and acceptance
11. Decisions CoS may veto

---

## 1. Scope and sequencing

- **This PR:** `docs/design/tui-color.md` only.
- **PR #37 lands first.** It adds mouse support, the sidebar thread groups, the profile picker and the header identity, plus CoS item A: `▌` section headers and amber unread polish. This design assumes #37's layout and glyphs. Its mocks show the `▌` headers in place.
- **Then the integrator.** One PR, stacked on #37, which:
  - adds `internal/tui/theme`;
  - moves every screen onto roles;
  - makes the structural changes in section 7;
  - regenerates the goldens and ui-shots.

  It changes no layout, keys or wording beyond section 7.
- **Out of scope:**
  - the web UI (its tokens are the source here, not a target);
  - layout changes;
  - a user-selectable list of themes;
  - per-user colour preferences.

## 2. Where the TUI is today

These are the findings at master `ace7bb8` and PR #37 `b92cc2a`. Each is a reason for a rule in this design.

| # | Finding | Where | Why it matters |
|---|---|---|---|
| T1 | The whole screen is painted: `v.BackgroundColor = colorBg` (`#0b0f19`) and `v.ForegroundColor = colorText` (`#f3f4f6`). | `model.go` `View()` | Breaks constraint C2. On a light terminal the TUI shows up as a dark slab. |
| T2 | Body text is forced to `#f3f4f6` (`styleText`). | `theme.go`, every screen | Once T1 is removed, that text disappears on a light terminal. Body text must use the terminal's default foreground. |
| T3 | The palette drifts from the web: muted `#6b7280` vs `--muted #9aa4b8`, good `#22c55e` vs `--ok #34d399`, warn `#eab308` vs `--wake #fbbf24`, bad `#ef4444` vs `--bad #fb7185`, border `#374151` vs `--line2 #2c3756`. | `theme.go` | The TUI and the web show the same state in different colours. |
| T4 | There are three selection fills: `colorPick #1e2a4a` (chat log, sidebar, picker), `inboxSelBg #1e293b` (inbox) and `colorDashCursor #1f2937` (dash). | `screen_chat.go`, `screen_inbox.go`, `screen_dash.go` | One role should cover all three. |
| T5 | Some colours are private to a screen. `screen_chat.go` holds `colorCyan`, `colorPick`, `styleAccepted` and six `styleSender`. `screen_dash.go` holds a filled `styleDashHead`, `styleDashConnected`, `styleDashReachable`, `colorDashCursor` and eight `styleDashKinds`. | `screen_chat.go`, `screen_dash.go` | None of them can be downgraded or switched to the light set from one place. |
| T6 | Focus is shown by colour only: a focused `Pane` just changes its border and title colour. | `layout.go` `Pane` | Breaks C3. Under `NO_COLOR` the focused pane cannot be told apart. |
| T7 | Inbox selection is shown by colour only: the selected row gets a background and no marker. | `screen_inbox.go` `viewRow` | Breaks C3. |
| T8 | Some colours say something untrue: | `screen_chat.go`, `screen_inbox.go`, `pick.go` | Each one weakens a colour's meaning. |
|  | • the sender palette includes `#34d399` (the web's ok green) and `#fb923c` (orange, which reads as a warning); |  |  |
|  | • the event log paints `presence` events amber; |  |  |
|  | • the picker paints `n` ("this session only") as a warning. |  |  |
| T9 | Colour is not chosen explicitly. lipgloss downsamples on its own. colorprofile parses `NO_COLOR` with `strconv.ParseBool`, so `NO_COLOR=yes` leaves colour on, although no-color.org says any non-empty value turns it off. It also treats `COLORTERM=yes` as truecolor. | colorprofile `env.go` | C1 is not guaranteed today. |

## 3. Constraints

These are normative. The integrator must follow them, and any later colour change must too.

**C1. Terminal support.**
- Truecolor when `COLORTERM` is `truecolor` or `24bit`.
- Otherwise 256 colours, when the terminal supports them.
- Otherwise the 16 basic colours.
- `NO_COLOR` removes all colour but keeps the structure: bold, reverse video, `▌` bars, `>` markers, rules and borders (heavy and light).

Section 4 gives the exact rule.

**C2. Never paint the whole background.**
- The terminal's own background shows everywhere except selection rows and the header bar.
- A filled cell always sets both its background and its foreground. It never relies on the terminal's foreground.
- Unfilled body text uses the terminal's default foreground.
- Colours must stay legible on both dark and light terminal themes (sections 4.3 and 9).

**C3. Colour is never the only signal.** Every state a colour shows is also shown by text or structure:
- counts and labels: `2 unread`, `dlq 3`, `live`, `paused_auth`;
- markers: `>`, `!`, `~`, `✓✓`, `✗`;
- the `▌` bar;
- bold, reverse video, or a heavy border.

If a colour were removed, no information would be lost.

**C4. Hostile text is never coloured by its content.**
- The theme wraps text that `Clean` has already sanitised.
- The role comes from which field the text sits in and from hub enums (state, kind, priority, health, event type, HTTP status). It never comes from matching words in peer-supplied text.
- No SGR sequence ever contains content.

Section 6.8 has the details.

**C5. Review evidence.** The integrator produces ui-shots of every TUI screen in the default theme, plus `NO_COLOR` and 16-colour variants of at least chat and inbox. CoS reviews them by eye before merging. Section 10 has the list.

## 4. Terminal capability and background

### 4.1 Profile rule

The theme works out the profile once, at start, in this order:

1. `NO_COLOR` is set and non-empty, whatever its value (`1`, `yes`, even `0`): **no colour**.
2. `COLORTERM` is `truecolor` or `24bit`, compared case-insensitively: **truecolor**.
3. colorprofile's terminal detection (`colorprofile.Detect` on stdout, with the environment) reports 256 or better: **256**. Only `TERM` decides this step. `COLORTERM=yes` and `COLORTERM=true` do not count as truecolor.
4. Otherwise: **16**.

Notes for the integrator:

- Pass the result to the program with `tea.WithColorProfile(p)`, so bubbletea's renderer and the theme agree. Then build every role value with `lipgloss.Complete(p)(ansi, ansi256, truecolor)`. Every rung gets the designed value, never a nearest-colour guess.
- tmux does not pass `COLORTERM` through by default, so an operator inside tmux usually gets the 256 rung. This is accepted: the 256 values are designed values, not a degraded copy. The fix on the operator's side is `set -ga update-environment COLORTERM`.
- The ui-shot harness runs with `COLORTERM=truecolor TERM=xterm-256color` and unsets `NO_COLOR` (`scripts/ui/tmux.mjs`), so the default shots are truecolor.

### 4.2 What each rung may emit

| Rung | Colour SGR | Attributes |
|---|---|---|
| truecolor | `38;2;r;g;b`, `48;2;r;g;b` | bold `1`, reverse `7` |
| 256 | `38;5;n`, `48;5;n`, with n from 16 to 255 only | bold, reverse |
| 16 | `31`–`36` (normal red to cyan), with reverse `7` for fills | bold, faint `2`, reverse |
| no colour | none: no `30`–`49`, `90`–`107`, `38;…` or `48;…` | bold, reverse |

The rungs have these limits:

- **256** never uses indices 0–15. Those follow the terminal theme, while 16–255 have fixed RGB values that this design has checked.
- **16** uses only the normal colours 1–6. Bright variants (9–15) are often unreadable on light themes, white (7, 15) disappears on light themes, and black (0, 8) disappears on dark ones. Muted text uses faint (`2`) instead of bright black (8): in Solarized Dark, colour 8 is the background colour itself.
- **No colour** does not use faint. Muted text there is plain.
- No rung uses underline, italic or blink.

### 4.3 Dark or light background

- At start the model sends `tea.RequestBackgroundColor`. When `tea.BackgroundColorMsg` arrives, `IsDark()` picks the dark or light set and the frame redraws.
- Until an answer arrives, and when none ever does, the **dark** set is used. Dark is vecnet's home look and the common operator setup.
- `VECNET_TUI_BG=dark` or `VECNET_TUI_BG=light` skips the query. This is the operator's fix for a terminal that does not answer. The light ui-shot needs it too, because tmux in the shot harness has no client terminal to answer the query.
- Only the truecolor and 256 rungs use the dark or light set. The 16-colour and no-colour rungs leave that choice to the terminal's own palette.

The light set exists because C2 keeps the terminal's background. For example, `--wake #fbbf24` on white has a contrast of about 1.7:1, while `#b45309` reaches 4.4–5.0:1 on light themes (section 9).

## 5. Semantic roles

Role names are the identifiers the integrator should use (`theme.Accent`, `theme.Unread` and so on).

### 5.1 Foreground roles

The table has these columns:

- **TC dark / TC light:** the truecolor values.
- **256 dark / 256 light:** the xterm-256 indices.
- **16:** the ANSI colour number.
- **None:** what the role becomes under `NO_COLOR`.
- **Code:** the letter used in the paint masks (section 8).

| Role | Code | From [CSS] | Used for | TC dark | TC light | 256 dark | 256 light | 16 | None |
|---|---|---|---|---|---|---|---|---|---|
| `text` | (blank) | `--text #f3f4f6` | Body text, names, values, titles. | default fg, no SGR | default fg | default | default | default | plain |
| `bold` | `x` | | Unread titles and rows, section labels, approval questions, notice heads. | default fg, bold | default, bold | bold | bold | bold | bold |
| `muted` | `m` | `--muted #9aa4b8` | Secondary text: times, field labels, hint words, meta lines, the unfocused pane title. | `#9aa4b8` | `#5f6a84` | 248 | 60 | faint | plain |
| `dim` | `d` | `--dim #5f6a84` | Ids, cursors, placeholders such as `-`, unknown presence. | `#5f6a84` | `#8391ad` | 60 | 103 | faint | plain |
| `accent` | `a` | `--grad` end `#a855f7` | `▌` bars, the `>` marker, the approval `?` gutter, the APR tag, the composer `›`, the `:` prompt, the moving notice. | `#a855f7` | `#7c3aed` | 135 | 92 | 5 | plain |
| `accent` + bold | `g` | | The focused pane's title. | `#a855f7` bold | `#7c3aed` bold | 135 bold | 92 bold | 5 bold | bold |
| `indigo` | `i` | `--grad` start, as text `.st-accepted #818cf8` | The accepted receipt `✓`, the `stored` pref source. | `#818cf8` | `#4f46e5` | 105 | 63 | 4 | plain |
| `action` | `v` | `--violet #a5b4fc` | Things a key does: `[1]`, `r resume`, `press a to answer`, picker `y` and `n`. | `#a5b4fc` | `#4f46e5` | 147 | 63 | 4 | plain |
| `unread` | `u` | `--wake #fbbf24` | Unread counts and badges, and attention counts on tabs. Always bold. | `#fbbf24` bold | `#b45309` bold | 214 bold | 130 bold | 3 bold | bold |
| `warn` | `w` | `--wake #fbbf24` | Needs attention soon: approval countdowns, `retry`, `late`, the `local` hub, wake priority, `apr N`, attempts. | `#fbbf24` | `#b45309` | 214 | 130 | 3 | plain |
| `success` | `s` | `--ok #34d399` | `live`, processed `✓✓`, health `ok`, connected, a positive toast. | `#34d399` | `#047857` | 43 | 29 | 2 | plain |
| `error` | `e` | `--bad #fb7185` | **Errors only**: DLQ, failed or dead `✗`, `off`, load failures, expired, failing subs, HTTP 400 and above, error toasts. | `#fb7185` | `#be123c` | 204 | 161 | 1 | plain |
| `info` | `n` | `--widle #38bdf8` | Wake_idle `~`, pushed `›`, files, reachable, neutral hub events, the `flag` pref source. | `#38bdf8` | `#0369a1` | 74 | 24 | 6 | plain |
| `hold` | `h` | `--hold #8391ad` | Hold priority. | `#8391ad` | `#64748b` | 103 | 67 | faint | plain |
| `rule` | `r` | `--line2`, raised to `#3d4a73` | Light borders, rules, the quote gutter `│`. | `#3d4a73` | `#b8c0d6` | 60 | 146 | faint | plain |
| `focus` | `f` | `--grad` end | The focused pane's heavy border. | `#a855f7` | `#7c3aed` | 135 | 92 | 5 | heavy glyphs only |

Why `rule` is raised: `--line #222b45` and `--line2 #2c3756` were tuned to sit on the web's `#0b0f19` panels. On other dark terminal backgrounds they almost vanish: against One Dark `#282c34` the contrast is 1.0–1.2:1. `#3d4a73` is the scrollbar-hover colour already in `base.css`. It stays quiet (1.6–2.4:1) but is visible on every reference background.

`unread` and `warn` share the amber hue, as the web does with `--wake`. Words, counts and bold tell them apart: unread is always bold and always a count. Neither is ever red.

### 5.2 Fill roles

These are the only places the TUI paints a background. Each fill names its own foreground.

| Fill | Code | Where | TC dark (bg / fg) | TC light (bg / fg) | 256 dark | 256 light | 16 | None |
|---|---|---|---|---|---|---|---|---|
| `selection` | `#` (or an upper-case role) | The selected row of a list. The current thread in the sidebar (its two lines). Every line of the selected chat message. It covers the pane's inner width, never the border. | `--line2 #2c3756` / `#f3f4f6` | `#c7d2fe` / `#111827` | 17 / 255 | 189 / 234 | reverse `7`; per-cell colours dropped | reverse |
| `bar` | gutter `H` | The header bar, the top row, across the full width. | `#1e2640` (the web's toast background) / `#f3f4f6`, muted `#9aa4b8` | `#e2e8f0` / `#111827`, muted `#5f6a84` | 236 / 255, muted 248 | 254 / 234, muted 60 | no fill | no fill |
| `tab` | `t` | The active tab label in the bar. | `#3d4a73` / `#f3f4f6`, bold | `#c7d2fe` / `#111827`, bold | 60 / 255 | 189 / 234 | reverse | reverse |
| `brand` | `b` | The ` vecnet ` chip in the header and the picker. | A per-cell gradient `#6366f1` → `#a855f7` across its 8 cells / `#ffffff`, bold | `#4f46e5` → `#7c3aed` / `#ffffff`, bold | 62 / 231 (solid) | 63 / 231 | 5 with reverse, bold | reverse, bold |
| `mode` | `c` | The status-line mode chip. | NORMAL `#6366f1` / `#ffffff`; INSERT `#a855f7` / `#ffffff`; COMMAND `#38bdf8` / `#0b0f19`; all bold | NORMAL `#4f46e5`, INSERT `#7c3aed`, COMMAND `#0369a1`, all / `#ffffff` | 62, 135, 74 / 231, 231, 16 | 63, 92, 24 / 231 | 4, 5, 6 with reverse, bold | reverse, bold |

The mode chips use the two ends of the brand gradient for NORMAL and INSERT, so the mode you type in is the brighter end. COMMAND takes the info hue. None of the chips uses amber or red, because those hues carry meaning.

In the 16-colour rung a chip is drawn as "role colour plus reverse": the cell's background becomes the ANSI colour and its text becomes the terminal's own background. That stays legible whichever way round the theme is.

### 5.3 Roles on a fill

**On `selection`:**
- `text`, `muted`, `dim`, `accent`, `indigo`, `action` and `hold` all take the fill's foreground.
- `unread`, `warn`, `success`, `error`, `info` and the identity hues keep their hue.
- Contrast on the dark fill is 4.4:1 for error, 5.5:1 for info, 7.0:1 for amber and 10.7:1 for text. On the light fill every hue is at least 3.4:1, and those cells are short bold tokens.
- In the 16-colour and no-colour rungs the whole row turns to reverse video and drops its per-cell colours. Glyphs and labels still carry the meaning.

**On `bar`:**
- `text` and `muted` take the bar's own foreground and muted values.
- `success` (`live`), `warn` (`apr`, `retry`, `local`) and `error` (`dlq`, `off`) keep their hue. Each one is at least 4.5:1 on the dark bar and 3.6:1 on the light bar.

### 5.4 Identity hues (people and kinds)

| Code | Hue | TC dark | TC light | 256 dark | 256 light | 16 |
|---|---|---|---|---|---|---|
| `1` | indigo | `#818cf8` | `#4f46e5` | 105 | 63 | 4 |
| `2` | purple | `#c084fc` | `#9333ea` | 141 | 92 | 5 |
| `3` | fuchsia | `#e879f9` | `#c026d3` | 213 | 164 | 5 |
| `4` | cyan | `#22d3ee` | `#0e7490` | 45 | 31 | 6 |
| `5` | blue | `#60a5fa` | `#2563eb` | 75 | 26 | 4 |

- **No red, amber, orange, green or pink.** Those hues would read as error, unread, warn or success. This drops the green, lime, teal, orange and pink entries from today's sender and kind palettes (`#34d399`, `#a3e635`, `#2dd4bf`, `#fb923c`, `#f472b6`).
- **Sender colour** is `fnv32a(peer id) mod 5`, using the hub-assigned peer id, never the display name. Today's `chatSenderStyle` already hashes the id, so only the palette changes. Sender names stay bold.
- **Kind colour** is used for the dash peer names and the kind label in the inbox detail:

  | Kinds | Colour |
  |---|---|
  | `operator` | `accent` |
  | `session` | 1 |
  | `pagchat-bot` | 2 |
  | `openclaw`, `dot` | 3 |
  | `grokbot` | 4 |
  | `launcher` | 5 |
  | `source` | `muted` |
  | any other kind | `text` |

  The KIND column and the name carry the meaning, so a shared hue costs nothing.
- **In the 16-colour rung** identities fold onto 4, 5 and 6. **Under no colour** sender names stay bold and everything else is plain.

### 5.5 State maps

Each state has one role, on every screen.

| Domain | Mapping |
|---|---|
| Priority (glyph and word) | wake `!` → `warn`; wake_idle `~` → `info`; hold (blank glyph) → `hold`. The status line's `prio wake` follows the same map. |
| Receipts | queued `·` → `muted`; pushed `›` → `info`; accepted `✓` → `indigo`; processed `✓✓` → `success`; read `●` or `◉` → `text`; failed and dead_letter `✗` → `error`; expired → `muted`. |
| Connection (header) | live → `success`; retry → `warn`; off → `error`. The label is always printed. |
| Presence (self-reported) | working → `text`; idle → `muted`; blocked → `warn`; offline and unknown → `dim`. |
| Reach (hub-observed) | connected → `success`; reachable → `info`; unreachable → `error`; unknown → `dim`. |
| Subscription health | ok → `success`; late, unverified, paused_operator → `warn`; failing, expired, paused_token, paused_failed, paused_auth → `error`; any value this client does not know → `muted`, because it "must not read as healthy or as broken" (`subsHealthStyle`). |
| Inbox tag | APR → `accent`; WAKE → `warn`; HOLD → `hold`; DLQ → `error`; SUB → its health role. An unread delivery row → `bold`; a read one → `muted`. |
| Event log type | receipt → `success`; `approval_*` → `accent`; failure types (`*_failed`, `dead_letter`) → `error`; everything else, including presence, thread, group and directory updates → `info`. Presence moves off amber. |
| Quota bar | above 25 % → `success`; 25 % or less → `warn`; 10 % or less → `error` (`dashWarnPct`, `dashLowPct`). Brackets → `dim`; `none` and `plan=…` → `muted`. |
| Header counts | `apr N` → `warn` when N > 0; `dlq N` → `error` when N > 0; both → `muted` at 0; `cuts/24h` → `muted`. Attention badges on tabs (`4:subs 8`) → `unread`. |
| Notices | moving → `accent` with a bold head; restart → `warn` with a bold head; other kinds → a bold `text` head with `muted` detail. |
| Thread budget | `cut N` → `error`; `0` → `warn`; otherwise `muted`. |
| HTTP status | 400 and above → `error`; otherwise `text`. |
| Prefs | source: default → `muted`, stored → `text`, flag and env → `info`. State: live → `success`, unavailable and lock → `warn`. A failed load → `error`. |
| Command line | an ok toast → `success`; an error toast → `error`; the quit-and-lose-draft question → `warn`. |
| Picker | the `local` marker → `warn`; the `y` and `n` keys → `action`. |

## 6. Composition rules

### 6.1 How to read the mocks

Each mock is a frame, followed by a **paint mask** of the same size, cell for cell, with two extra columns at the left: a gutter letter and a space.

- **Gutter:**
  - `H` means the row is the header bar fill.
  - `S` means the row holds a selection fill.
- **Codes:** the letters from the tables in sections 5.1 and 5.2, and the digits from 5.4.
- **Blank:** the terminal's default foreground with no SGR.
- **Upper case** (`A`, `M`, `W`, `E`, `S` and so on): that role drawn on the selection fill, using the section 5.3 rules.
- **`#`:** default text on the selection fill.
- **`b`, `t`, `c`:** the brand, active-tab and mode chips.

The inbox and chat mocks add two more masks:

- **`NO_COLOR`:** `b` bold, `R` reverse, `V` reverse and bold. Heavy borders, `▌`, `>` and every glyph stay.
- **16 colours:** digits are the ANSI colours 1–6, `~` is faint, `R` is reverse with no per-cell colour, and `V` is the role's colour shown in reverse. Bold follows the same rules as in truecolor.

### 6.2 Text and background

- Body text sets no foreground. This fixes T2.
- The frame sets no background and no foreground: `View()` must not set `BackgroundColor` or `ForegroundColor`. This fixes T1.
- Only the fills in section 5.2 set a background. Each of them sets a foreground as well.

### 6.3 Bold

Bold is used only for:

- unread titles, rows and counts;
- section labels (after `▌`);
- the focused pane's title;
- sender names in the chat log;
- approval question text;
- notice heads;
- the chips.

Nothing else is bold, so bold keeps its meaning under `NO_COLOR`.

### 6.4 Focus

- The focused pane draws a **heavy border** (`┏━┓┃┗┛`, `lipgloss.ThickBorder()`) in `focus`, with its title in accent and bold.
- Every other pane draws a light border (`┌─┐│└┘`, `lipgloss.NormalBorder()`) in `rule`, with a `muted` title.
- Exactly one pane per frame is heavy. A full-screen pane such as help or prefs counts as focused.
- Heavy and light box glyphs have the same width, so no layout moves.

### 6.5 Selection

- The selected row starts with `>` in `accent` and gets the `selection` fill across the pane's inner width.
- The inbox puts `>` in the blank it already has at column 1 (fixes T7).
- Dash, subs, prefs and the picker already have a `> ` column.
- In the chat sidebar the current thread has no marker column. Its fill (reverse when there is no colour) and the log pane's title, which names the thread, carry the signal.
- In the chat log, every line of the selected message is filled. A blank line between two messages stays unfilled, as `chatRow.render` does today.

### 6.6 Section headers

The `▌` header comes from PR #37 (CoS A): `▌` in `accent`, then the label in `bold`. It is used for:

- the sidebar groups (Your Threads, Other Threads);
- the prefs groups;
- the help sections;
- the subsections of dash delivery health (loop cuts, counters).

`▌` stays under `NO_COLOR`.

### 6.7 Unread

- An unread count is drawn in `unread` (amber, bold).
- Its title or row is drawn in `bold`.
- Read items are `text` (in lists) or `muted` (inbox rows).
- Unread is never red, and never shown by colour alone: the count is always printed.

### 6.8 Hostile text

Hostile text is anything a peer or a hub admin can write:

- peer names;
- thread titles;
- message bodies;
- approval questions and options;
- notice text;
- preference values;
- error strings relayed from peers.

The rules:

1. `Clean` runs first. The theme only wraps strings that `Clean` has returned. The wrapper is one `Style.Render` per segment, so a reset cannot leak into the next segment (this is today's `chatRow` pattern).
2. The role comes from the field (sender, body, title, question) or from a hub enum (receipt state, priority, health, kind, event type, HTTP status). Content is never matched: no highlighting of `error`, `failed`, URLs, mentions or markdown inside bodies.
3. Content never goes into an escape sequence, and no ANSI from content survives (`Clean` strips it).
4. Required tests:
   - a peer named `\x1b[31mERROR` renders as plain `text` with no SGR from the name;
   - a body that reads `dead_letter failed` renders in `text`, not `error`.

### 6.9 Do not

- Paint the whole background, or give body text a foreground.
- Add a hex value, a 256 index or an ANSI number outside `internal/tui/theme`.
- Use red for unread or for attention, or amber for errors.
- Show any state by colour alone.
- Use italic, underline or blink. Terminal support for them is uneven, and the shot renderer would hide differences between terminals.

## 7. Structural changes the integrator makes

These are the only changes that are not colour. Each one exists for C2 or C3.

1. `View()` stops setting `BackgroundColor` and `ForegroundColor` (C2, T1).
2. `Pane` takes a heavy border when focused (C3, T6). This changes plain-text goldens wherever a pane is focused.
3. The selected inbox row gets `>` in column 1 (C3, T7). This changes the inbox goldens.
4. The dash table head loses its indigo fill. It becomes `muted`, matching subs (C2: fills are only for selection and the header bar).
5. Status-line hints draw the key in `text` and its word in `muted`. `Hints()` strings already separate pairs with two spaces, and the first token of each pair is the key.
6. The picker's `y` and `n` keys use `action` instead of `success` and `warn` (T8).

Layout, wording and keys stay as they are.

## 8. Mock frames, per screen group

The frames follow PR #37's goldens: the same columns, labels and truncation, with the section 7 changes applied. Inbox, dash, subs, prefs and help are 80 columns wide. Chat is 100 columns wide, so that the sidebar and the who list read properly. The wide header is 140 columns, the shot size. The integrator's ui-shots at real sizes are the final check.

### 8.1 Header and help

The header bar at 140 columns (the shot size), with all segments present. `4:subs 8` is an attention badge.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs 8                              home · hub hub.example:8443  live  apr 2  dlq 3  cuts/24h 0  07:41:00
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmtttttttmmmmmmmmmmmmmmmmmmmmmmu                              mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss  wwwww  eeeee  mmmmmmmmmm
```

Header states at 80 columns. A loopback hub is marked `local` in `warn` and the connection is retrying (first row). The connection is off (second row).

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs     dev · hub 127.0.0.1:8443 local  retry
 vecnet | 1:inbox 2:chat 3:dash 4:subs       home · hub hub.example:8443  off
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmtttttttmmmmmmmmmmmmmmmmmmmmm     mmmmmmmmmmmmmmmmmmmmmmmmmwwwww  wwwww
H bbbbbbbbmmtttttttmmmmmmmmmmmmmmmmmmmmm       mmmmmmmmmmmmmmmmmmmmmmmmmmm  eee
```

The notice row sits under the header and is not filled. Its kinds are moving, restart and other.

```text
! MOVING in 18h Nov 03 02:00 -> vecnet.xk.io  Moving to Fly tonight  (+1 more)
! RESTART in 4m 07:45  Hub restarts for an upgrade
! NOTICE in 2d  Quota counters reset on Monday
```

Paint (truecolor and 256; codes in section 6.1):

```text
  aaaaaaaa aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa                         mmmmmmmmm
  wwwwwwwww wwwwwwwwwww
  xxxxxxxx mmmmm
```

Help is a full-screen pane, so it is focused (heavy border):

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs       home · hub hub.example:8443  live
┏━ help ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃▌global keys                                                                  ┃
┃  1-4     switch tab                                                          ┃
┃  q       quit                                                                ┃
┃  :       command line                                                        ┃
┃  ?       help; M releases mouse; Shift+drag selects text                     ┃
┃  ctrl+l  reload everything                                                   ┃
┃▌inbox                                                                        ┃
┃  j/k     move                                                                ┃
┃  a       answer                                                              ┃
┃▌commands                                                                     ┃
┃  :mouse on|off   release; Shift+drag selects text                            ┃
┃  :prefs          show read-only hub preferences                              ┃
┃  :presence <s>   set your presence (idle|working|blocked|offline)            ┃
┃  :retry all      retry every listed dead letter                              ┃
┃  :tab <name>     switch tab                                                  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
 NORMAL   j/k move  a answer                   max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmtttttttmmmmmmmmmmmmmmmmmmmmm       mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss
  ff gggg ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  faxxxxxxxxxxx                                                                  f
  f          mmmmmmmmmm                                                          f
  f          mmmm                                                                f
  f          mmmmmmmmmmmm                                                        f
  f          mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                     f
  f          mmmmmmmmmmmmmmmmm                                                   f
  faxxxxx                                                                        f
  f          mmmm                                                                f
  f          mmmmmm                                                              f
  faxxxxxxxx                                                                     f
  f                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                            f
  f                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                              f
  f                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm            f
  f                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                              f
  f                  mmmmmmmmmm                                                  f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  cccccccc      mmmm    mmmmmm                   mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

### 8.2 Inbox

The list is focused and the first approval is selected. `2 unread` in the title is amber and bold. The unread WAKE and HOLD rows are bold, and the read HOLD row is muted. The quote gutter in the detail pane uses `rule`.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs       home · hub hub.example:8443  live
┏━ inbox 12 items · 2 unread · apr 2 · dlq 3 ━━━━┓┌─ detail ───────────────────┐
┃>APR  07:52 dot-rese… Spend about 2… exp 11:39  ┃│ approval_request  wake     │
┃ APR  07:59 claw-ops  Push classifi… exp 18:59  ┃│                            │
┃ DLQ  04:11 grok-tri… 503 retries e… 8 tries    ┃│ from     dot-research dot  │
┃ SUB  07:40 dot-rese… paused_auth               ┃│ thread   Hosting cost deep │
┃ SUB  07:40 dot-rese… paused_operat… r resume   ┃│ message  msg_01JD3S6G8J0M2P│
┃ WAKE 06:12 codex-ha… The radio lis… ›          ┃│                            │
┃ HOLD 07:39 codex-ha… Ticket 18 pro… ◉          ┃│ ? Spend about 20% of my    │
┃ HOLD 06:31 pag-hark… Reproduced on… ✓          ┃│ ? weekly quota on a deep-  │
┃                                                ┃│ ? research run?            │
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛│   [1] yes                  │
┌─ your event log  GET /v1/events -f · not messag┐│   [2] no                   │
│ e1.10401  07:41:00 presence          pag-hark-…││                            │
│ e1.10403  07:41:00 thread_updated    thr_…XP5R…││ expires 07:52, in 11:39    │
│ e1.10404  07:41:00 receipt           msg_…0X2Z…││ press a to answer          │
│ e1.10405  07:41:00 approval_expired  apr_…0X7H…││                            │
│ e1.10406  07:41:00 group_updated     {"group_i…││ │ A thorough comparison    │
│ e1.10407  07:41:00 directory_updated codex-har…││ │ needs a deep-research    │
│                                                ││ │ run. That costs about    │
│                                                ││ … 2 more lines in thread   │
└────────────────────────────────────────────────┘└────────────────────────────┘
 NORMAL   a answer  r retry  x drop  p status  max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmtttttttmmmmmmmmmmmmmmmmmmmmm       mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss
  ff ggggg gg ggggg g uuuuuuuu g wwwww g eeeee fffffrr mmmmmm rrrrrrrrrrrrrrrrrrrr
S fAAAA##MMMMM##########################WWWWWWWWW##fr aaaaaaaaaaaaaaaa  wwww     r
  f aaa  mmmmm                          wwwwwwwww  fr                            r
  f eee  mmmmm           eee mmmmmmmmmm mmmmmmm    fr mmmm                  333  r
  f eee  mmmmm           eeeeeeeeeee               fr mmmmmm                     r
  f www  mmmmm           wwwwwwwwwwwwww vvvvvvvv   fr mmmmmmm  ddddddddddddddddddr
  f wwww mmmmm           xxxxxxxxxxxxxx n          fr                            r
  f hhhh mmmmm           mmmmmmmmmmmmmm            fr a xxxxxxxxxxxxxxxxxxxxx    r
  f hhhh mmmmm           xxxxxxxxxxxxxx i          fr a xxxxxxxxxxxxxxxxxxxxxxx  r
  f                                                fr a xxxxxxxxxxxxx            r
  ffffffffffffffffffffffffffffffffffffffffffffffffffr   vvv                      r
  rr mmmm mmmmm mmm  mmm mmmmmmmmmm mm m mmm mmmmmmrr   vvv                      r
  r dddddddd  mmmmmmmm nnnnnnnn                    rr                            r
  r dddddddd  mmmmmmmm nnnnnnnnnnnnnn              rr mmmmmmmmmmmmmmmmmmwwwww    r
  r dddddddd  mmmmmmmm sssssss                     rr vvvvvvvvvvvvvvvvv          r
  r dddddddd  mmmmmmmm aaaaaaaaaaaaaaaa            rr                            r
  r dddddddd  mmmmmmmm nnnnnnnnnnnnn               rr r mmmmmmmmmmmmmmmmmmmmm    r
  r dddddddd  mmmmmmmm nnnnnnnnnnnnnnnnn           rr r mmmmmmmmmmmmmmmmmmmmm    r
  r                                                rr r mmmmmmmmmmmmmmmmmmmmm    r
  r                                                rr mmmmmmmmmmmmmmmmmmmmmmmm   r
  rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr
  cccccccc    mmmmmm    mmmmm    mmmm    mmmmmm  mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

`NO_COLOR` attributes (b bold, R reverse, V reverse and bold; heavy borders stay):

```text
  VVVVVVVV  RRRRRRR
     bbbbb bb bbbbb b bbbbbbbb b       b
S  RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR




                         bbbbbbbbbbbbbb
                                                        bbbbbbbbbbbbbbbbbbbbb
                         bbbbbbbbbbbbbb                 bbbbbbbbbbbbbbbbbbbbbbb
                                                        bbbbbbbbbbbbb











  VVVVVVVV

```

16 colours (ANSI index 1-6, ~ faint, R reverse with no per-cell colour, V reverse of the role colour):

```text
  VVVVVVVV~~RRRRRRR~~~~~~~~~~~~~~~~~~~~~       ~~~~~~~~~~~~~~~~~~~~~~~~~~~  2222
  55 55555 55 55555 5 33333333 5 33333 5 11111 55555~~ ~~~~~~ ~~~~~~~~~~~~~~~~~~~~
S 5RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR5~ 5555555555555555  3333     ~
  5 555  ~~~~~                          333333333  5~                            ~
  5 111  ~~~~~           111 ~~~~~~~~~~ ~~~~~~~    5~ ~~~~                  555  ~
  5 111  ~~~~~           11111111111               5~ ~~~~~~                     ~
  5 333  ~~~~~           33333333333333 44444444   5~ ~~~~~~~  ~~~~~~~~~~~~~~~~~~~
  5 3333 ~~~~~                          6          5~                            ~
  5 ~~~~ ~~~~~           ~~~~~~~~~~~~~~            5~ 5                          ~
  5 ~~~~ ~~~~~                          4          5~ 5                          ~
  5                                                5~ 5                          ~
  55555555555555555555555555555555555555555555555555~   444                      ~
  ~~ ~~~~ ~~~~~ ~~~  ~~~ ~~~~~~~~~~ ~~ ~ ~~~ ~~~~~~~~   444                      ~
  ~ ~~~~~~~~  ~~~~~~~~ 66666666                    ~~                            ~
  ~ ~~~~~~~~  ~~~~~~~~ 66666666666666              ~~ ~~~~~~~~~~~~~~~~~~33333    ~
  ~ ~~~~~~~~  ~~~~~~~~ 2222222                     ~~ 44444444444444444          ~
  ~ ~~~~~~~~  ~~~~~~~~ 5555555555555555            ~~                            ~
  ~ ~~~~~~~~  ~~~~~~~~ 6666666666666               ~~ ~ ~~~~~~~~~~~~~~~~~~~~~    ~
  ~ ~~~~~~~~  ~~~~~~~~ 66666666666666666           ~~ ~ ~~~~~~~~~~~~~~~~~~~~~    ~
  ~                                                ~~ ~ ~~~~~~~~~~~~~~~~~~~~~    ~
  ~                                                ~~ ~~~~~~~~~~~~~~~~~~~~~~~~   ~
  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
  VVVVVVVV    ~~~~~~    ~~~~~    ~~~~    ~~~~~~  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~3333
  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
```

### 8.3 Chat and sidebar

The log is focused, and the selected message (pag-hark-sleuth, 06:31) is filled across all its lines. In the sidebar, the current thread's two lines are filled, unread titles are bold with an amber count, and both groups carry `▌` headers. The `■` swatches and sender names use the identity hues. The receipts and priority legends use their state roles.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs             home · hub hub.example:8443  live  apr 1  dlq 0
┌─ threads [ ] ──────┐┏━ hark B196: radio…  thr_…3P5R · budget 494 ━━━━┓┌─ who ────────────────────┐
│▌Your Threads       │┃06:12 !<codex-hark> The radio listen did not    ┃│peer            pres reach│
│hark B196: radio… 2 │┃       finalize on "over" in session d211530e.  ┃│■ codex-hark    work conn │
│codex-hark · 6m · 9 │┃       file hark-listen.log 18.0 KiB            ┃│■ pag-hark-sle… work conn │
│Ticket 18: operator…│┃       grok✓✓ max●                              ┃│■ claw-ops      idle conn │
│codex-hark · 1h · 14│┃                                                ┃│■ dot-research  unkn unrch│
│Hosting cost de… 12 │┃06:31 ~<pag-hark-sleuth> ↳codex@06:12 Reproduced┃│■ grok-reviewer unkn reach│
│dot-research · 3h   │┃       on x-tuf. Root cause is in the end-phrase┃│                          │
│                    │┃       classifier.                              ┃│files:                    │
│▌Other Threads +    │┃       [+14.3 KiB spilled → message.md]         ┃│hark-listen.log 18.0 KiB  │
│                    │┃       codex✓✓ grok✓ max●                       ┃│message.md 15.6 KiB       │
│                    │┃                                                ┃│receipts:                 │
│                    │┃-- 07:03 approval expired                       ┃│· queued  › pushed        │
│                    │┃                                                ┃│✓ accepted                │
│                    │┃07:05 !<claw-ops> ↳codex@07:01 May I push the   ┃│✓✓ processed              │
│                    │┃       classifier fix to ultradyn/hark master?  ┃│● read  ✗ dead            │
│                    │┃       ? Push and release? [1]yes [2]no         ┃│                          │
│                    │┃       exp 08:10 29m                            ┃│priority:                 │
│                    │┃       max✓ pag· claw✗                          ┃│! wake  ~ wake_idle       │
└────────────────────┘┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛└──────────────────────────┘
hark B196: radio end-ph… › i to write, or :msg <peer> <text>
 NORMAL   i write  [ ] thread  j/k select  R reply  a answer       max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmmmmmmmmmttttttmmmmmmmmmmmmmm             mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss  wwwww  mmmmm
  rr mmmmmmm mmm rrrrrrrff gggg ggggg gggggg  mmmmmmmmmmmmmmmmmmmmmm fffffrr mmm rrrrrrrrrrrrrrrrrrrrr
  raxxxxxxxxxxxx       rfmmmmm w111111111111                             frmmmmmmmmmmmmmmmmmmmmmmmmmmr
S rXXXXXXXXXXXXXXXXX#U#rf                                                fr1                    ssss r
S rMMMMMMMMMMMMMMMMMMM#rf       nnnnnnnnnnnnnnnnnnnn mmmmmmmm            fr2                    ssss r
  r                    rf       mmmmss mmm                               fr4               mmmm ssss r
  rmmmmmmmmmmmmmmmmmmmmrf                                                fr3               dddd eeeeer
S rxxxxxxxxxxxxxxxx uu rfMMMMM#N22222222222222222#MMMMMMMMMMMM###########fr5               dddd nnnnnr
S rmmmmmmmmmmmmmmmmm   rf################################################fr                          r
S r                    rf################################################frmmmmmm                    r
S raxxxxxxxxxxxxx m    rf#######MMMMMMMMMMMMMMMMMMMMMNNNNNNNNNNM#########frnnnnnnnnnnnnnnn mmmmmmmm  r
S r                    rf#######MMMMMSS#MMMMI#MMM########################frnnnnnnnnnn mmmmmmmm       r
  r                    rf                                                frmmmmmmmmm                 r
  r                    rfmmmmmmmmmmmmmmmmmmmmmmmmm                       frmmmmmmmm  n mmmmmm        r
  r                    rf                                                fri mmmmmmmm                r
  r                    rfmmmmm w4444444444 mmmmmmmmmmmm                  frss mmmmmmmmm              r
  r                    rf                                                fr  mmmm  e mmmm            r
  r                    rf       a                   vvv    vvv           fr                          r
  r                    rf       wwwwwwwwwwwww                            frmmmmmmmmm                 r
  r                    rf       mmmi mmmm mmmme                          frw mmmm  n mmmmmmmmm       r
  rrrrrrrrrrrrrrrrrrrrrrffffffffffffffffffffffffffffffffffffffffffffffffffrrrrrrrrrrrrrrrrrrrrrrrrrrrr
  mmmmmmmmmmmmmmmmmmmmmmmm a mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
  cccccccc    mmmmm      mmmmmm      mmmmmm    mmmmm    mmmmmm       mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

`NO_COLOR` attributes (b bold, R reverse, V reverse and bold; heavy borders stay):

```text
  VVVVVVVV          RRRRRR
                           bbbb bbbbb bbbbbb
    bbbbbbbbbbbb                bbbbbbbbbbbb
S  RRRRRRRRRRRRRRRRRRRR
S  RRRRRRRRRRRRRRRRRRRR


S  bbbbbbbbbbbbbbbb bb   RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR
S                        RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR
S                        RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR
S   bbbbbbbbbbbbb        RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR
S                        RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR



                                bbbbbbbbbb






  VVVVVVVV

```

16 colours (ANSI index 1-6, ~ faint, R reverse with no per-cell colour, V reverse of the role colour):

```text
  VVVVVVVV~~~~~~~~~~RRRRRR~~~~~~~~~~~~~~             ~~~~~~~~~~~~~~~~~~~~~~~~~~~  2222  33333  ~~~~~
  ~~ ~~~~~~~ ~~~ ~~~~~~~55 5555 55555 555555  ~~~~~~~~~~~~~~~~~~~~~~ 55555~~ ~~~ ~~~~~~~~~~~~~~~~~~~~~
  ~5                   ~5~~~~~ 3444444444444                             5~~~~~~~~~~~~~~~~~~~~~~~~~~~~
S ~RRRRRRRRRRRRRRRRRRRR~5                                                5~4                    2222 ~
S ~RRRRRRRRRRRRRRRRRRRR~5       66666666666666666666 ~~~~~~~~            5~5                    2222 ~
  ~                    ~5       ~~~~22 ~~~                               5~6               ~~~~ 2222 ~
  ~~~~~~~~~~~~~~~~~~~~~~5                                                5~5               ~~~~ 11111~
S ~                 33 ~5RRRRRRR55555555555555555RRRRRRRRRRRRRRRRRRRRRRRR5~4               ~~~~ 66666~
S ~~~~~~~~~~~~~~~~~~   ~5RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR5~                          ~
S ~                    ~5RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR5~~~~~~~                    ~
S ~5              ~    ~5RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR5~666666666666666 ~~~~~~~~  ~
S ~                    ~5RRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR5~6666666666 ~~~~~~~~       ~
  ~                    ~5                                                5~~~~~~~~~~                 ~
  ~                    ~5~~~~~~~~~~~~~~~~~~~~~~~~~                       5~~~~~~~~~  6 ~~~~~~        ~
  ~                    ~5                                                5~4 ~~~~~~~~                ~
  ~                    ~5~~~~~ 36666666666 ~~~~~~~~~~~~                  5~22 ~~~~~~~~~              ~
  ~                    ~5                                                5~  ~~~~  1 ~~~~            ~
  ~                    ~5       5                   444    444           5~                          ~
  ~                    ~5       3333333333333                            5~~~~~~~~~~                 ~
  ~                    ~5       ~~~4 ~~~~ ~~~~1                          5~3 ~~~~  6 ~~~~~~~~~       ~
  ~~~~~~~~~~~~~~~~~~~~~~55555555555555555555555555555555555555555555555555~~~~~~~~~~~~~~~~~~~~~~~~~~~~
  ~~~~~~~~~~~~~~~~~~~~~~~~ 5 ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
  VVVVVVVV    ~~~~~      ~~~~~~      ~~~~~~    ~~~~~    ~~~~~~       ~~~~~~~~~~~~~~~~~~~~~~~~~~~~3333
  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
```

### 8.4 Dash

The peers table is focused, with `max` selected. Peer names take their kind colour, presence and reach use the section 5.5 maps, and the quota bar's colour follows its level. The table head is muted and has no fill.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs       home · hub hub.example:8443  live
┏━ peers · 7 · presence is self-reported, reach is hub-observed ━━━━━━━━━━━━━━━┓
┃  PEER                   PRESENCE  REACH        QUOTA                LAST SEEN┃
┃> max                    working   connected    none                 0s ago   ┃
┃  codex-hark             working   connected    [||||      ]  44%    0s ago   ┃
┃  dot-research           unknown   reachable    [||||      ]  35%    18m ago  ┃
┃  grok-triage            unknown   unreachable  [          ]   3%    3h ago   ┃
┃  claw-ops               idle      connected    plan=pro tokens=1200 0s ago   ┃
┃  gh-ultradyn            n/a       reachable    none                 39m ago  ┃
┃  old-bot       disabled idle      unknown      none                 30h ago  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
┌─ threads · 4 ───────────┐┌─ delivery health ───────┐┌─ audit tail -f ────────┐
│  vecnet ticket … 499    ││grok-triage · Weekly quot││07:22:00 approval.open  │
│  Hosting cost d… 497    ││  blocked on dly_…3A5C   ││07:18:00 delivery.confl…│
│  Weekly quota r… cut 484││  attempt 3 · 2 waiting  ││05:48:00 thread.rename  │
│  Nightly digest… 0      ││409 dot-research HTTP    ││                        │
│                         ││                         ││                        │
│                         ││▌loop cuts / 24h 7       ││                        │
│                         ││  max_depth_exceeded 4   ││                        │
│                         ││  thread_budget_exh… 2   ││                        │
│                         ││  expired            1   ││                        │
└─────────────────────────┘└─────────────────────────┘└────────────────────────┘
 NORMAL   j/k peer  enter threads  tab pane    max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmmmmmmmmmmmmmmmmttttttmmmmmmm       mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss
  ff ggggg g g g mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm ffffffffffffffff
  f  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmf
S fA#AAA##############################SSSSSSSSS####MMMM#################MMMMMM###f
  f  1111111111                       sssssssss    dssssddddddd  sss    mmmmmm   f
  f  333333333333           ddddddd   nnnnnnnnn    dssssddddddd  sss    mmmmmmm  f
  f  44444444444            ddddddd   eeeeeeeeeee  dddddddddddd   ee    mmmmmm   f
  f  33333333               mmmm      sssssssss    mmmmmmmmmmmmmmmmmmmm mmmmmm   f
  f  mmmmmmmmmmm            ddd       nnnnnnnnn    mmmm                 mmmmmmm  f
  f  mmmmmmm       eeeeeeee mmmm      ddddddd      mmmm                 mmmmmmm  f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  rr mmmmmmm m m rrrrrrrrrrrrrr mmmmmmmm mmmmmm rrrrrrrrrr mmmmm mmmm mm rrrrrrrrr
  r                  mmm    rr                         rrmmmmmmmm                r
  r                  mmm    rr  wwwwwwwwww ddddddddd   rrmmmmmmmm                r
  r                  eeeeeeerr  mmmmmmmmmmmmmmmmmmmmm  rrmmmmmmmm                r
  r                  w      rreee              mmmm    rr                        r
  r                         rr                         rr                        r
  r                         rraxxxxxxxxxxxxxxx w       rr                        r
  r                         rr  mmmmmmmmmmmmmmmmmm     rr                        r
  r                         rr  mmmmmmmmmmmmmmmmmm     rr                        r
  r                         rr  mmmmmmm                rr                        r
  rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr
  cccccccc      mmmm        mmmmmmm      mmmm    mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

### 8.5 Subs

`failed-dot` is selected. Health, expiry, the last status and attempts use their maps, and the detail pane opens with the error reason.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs 8     home · hub hub.example:8443  live
┏━ subs · 9 rows ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃  peer         health           expires in           last refresh  last status┃
┃  fine-dot     ok               1h 0m                02/11 07:36   200        ┃
┃  failing-dot  failing          1h 0m                02/11 07:36   503        ┃
┃  late-dot     late             4m                   02/11 07:36   200        ┃
┃  expired-dot  expired          expired 2h 5m ago    02/11 07:36   200        ┃
┃  token-dot    paused_token     expired 1m ago       02/11 07:36   200        ┃
┃> failed-dot   paused_failed    1h 0m                02/11 07:36   500        ┃
┃  auth-dot     paused_auth      1h 0m                never         200        ┃
┃  operator-do  paused_operator  1h 0m                02/11 07:36   200        ┃
┃  unverified-  unverified       -                    never         -          ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
┌─ failed-dot · paused_failed ─────────────────────────────────────────────────┐
│One event used all 8 attempts. Fix the callback, then resume. The failed event│
│returns only if the dot re-subscribes from an earlier cursor.                 │
│paused: delivery_failed                                                       │
│status 500  attempts 8  cursor e0.42                                          │
│sub sub_failed  host failed.chatgpt.example  name vecnet.message              │
│expires 02/11/2026 08:41:00  refresh before 02/11/2026 08:31:00               │
│verified 02/11/2026 06:41:00  last delivery 02/11/2026 07:40:00               │
└──────────────────────────────────────────────────────────────────────────────┘
 NORMAL   j/k move  read-only                  max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmmmmmmmmmmmmmmmmmmmmmmmtttttt u     mmmmmmmmmmmmmmmmmmmmmmmmmmm  ssss
  ff gggg g g gggg fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  f  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmf
  f               ss                                    mmmmmmmmmmm              f
  f               eeeeeee                               mmmmmmmmmmm   eee        f
  f               wwww             ww                   mmmmmmmmmmm              f
  f               eeeeeee          eeeeeeeeeeeeeeeee    mmmmmmmmmmm              f
  f               eeeeeeeeeeee     eeeeeeeeeeeeee       mmmmmmmmmmm              f
S fA##############EEEEEEEEEEEEE#########################MMMMMMMMMMM###EEE########f
  f               eeeeeeeeeee                           mmmmm                    f
  f               wwwwwwwwwwwwwww                       mmmmmmmmmmm              f
  f               wwwwwwwwww       d                    mmmmm         d          f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  rr mmmmmmmmmm m eeeeeeeeeeeee rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr
  r                                                                              r
  r                                                                              r
  reeeeeeeeeeeeeeeeeeeeeee                                                       r
  rmmmmmmmeeemmmmmmmmmmmwmmmmmmmmmmmmmm                                          r
  rmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm              r
  rmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm               r
  rmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm               r
  rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr
  cccccccc      mmmm  mmmmmmmmm                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

### 8.6 Command line and status line

The rows below are, in order:

1. the NORMAL status line;
2. the idle command row;
3. the INSERT status line;
4. the composer while typing (`▏` is the cursor);
5. the COMMAND status line;
6. the command being typed;
7. an ok toast;
8. an error toast;
9. the quit-and-lose-draft question.

```text
 NORMAL   a answer  r retry  x drop  p status  max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
 INSERT   enter send  esc normal               max@vecnet · working · prio wake
hark B196: radio end-ph… › Patched on x-tuf too▏
 COMMAND   tab complete  enter run  esc cancel max@vecnet · working · prio wake
:retry all▏
retried 3 dead letters
unknown command: retyr  (:help lists commands)
quit and lose this draft? y/n
```

Paint (truecolor and 256; codes in section 6.1):

```text
  cccccccc    mmmmmm    mmmmm    mmmm    mmmmmm  mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
  cccccccc        mmmm      mmmmmm               mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmm a
  ccccccccc      mmmmmmmm        mmm      mmmmmm mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  a
  ssssssssssssssssssssss
  eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
  wwwwwwwwwwwwwwwwwwwwwwwwwwwww
```

### 8.7 Prefs

Prefs replaces the active tab with a `prefs` tab chip and fills the body. Group headers use `▌`, and the selected row is filled.

```text
 vecnet | 1:inbox 2:chat 3:dash 4:subs prefs       home·hub.example:8443  live
┏━ preferences ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃▌Retention & storage                                                          ┃
┃> retention.ack_events             7d       default  unavailable              ┃
┃▌Files                                                                        ┃
┃  blobs.expiry                     1d       default  unavailable              ┃
┃▌Notifications/presence                                                       ┃
┃  events.refresh_interval          30m      flag     live                     ┃
┃  presence.default_ttl             1h       stored   live                     ┃
┃▌Agents defaults                                                              ┃
┃  agents.thread_budget             500      default  live                     ┃
┃                                                                              ┃
┃Acknowledged event retention                                                  ┃
┃Keep acknowledged operational events for this long; zero disables pruning.    ┃
┃Owner floors and pins still apply.                                            ┃
┃3600..31536000 seconds                                                        ┃
┃source default                                                                ┃
┃unavailable                                                                   ┃
┃lock: DB retention adapter is not installed.                                  ┃
┃                                                                              ┃
┃                                                                              ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
 NORMAL   j/k move  r refresh                  max@vecnet · working · prio wake
: command  ? help  Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
H bbbbbbbbmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmttttt       mmmmmmmmmmmmmmmmmmmmm  ssss
  ff ggggggggggg fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  faxxxxxxxxxxxxxxxxxxx                                                          f
S fA###########################################MMMMMMM##WWWWWWWWWWW##############f
  faxxxxx                                                                        f
  f                                            mmmmmmm  wwwwwwwwwww              f
  faxxxxxxxxxxxxxxxxxxxxxx                                                       f
  f                                            nnnn     ssss                     f
  f                                                     ssss                     f
  faxxxxxxxxxxxxxxx                                                              f
  f                                            mmmmmmm  ssss                     f
  f                                                                              f
  fxxxxxxxxxxxxxxxxxxxxxxxxxxxx                                                  f
  f                                                                              f
  f                                                                              f
  fmmmmmmmmmmmmmmmmmmmmmm                                                        f
  fmmmmmmmmmmmmmm                                                                f
  fwwwwwwwwwww                                                                   f
  fwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwwww                                  f
  f                                                                              f
  f                                                                              f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  cccccccc      mmmm    mmmmmmm                  mmmmmmmmmmmmmmmmmmmmmmmmmmmmwwww
  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
```

The error state and the unavailable state:

```text
┏━ preferences ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃could not load preferences                                                    ┃
┃GET /v1/admin/prefs: 503 Service Unavailable                                  ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
┏━ preferences ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃preferences unavailable                                                       ┃
┃this hub predates hub preferences                                             ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

Paint (truecolor and 256; codes in section 6.1):

```text
  ff ggggggggggg fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  feeeeeeeeeeeeeeeeeeeeeeeeee                                                    f
  fmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                                  f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  ff ggggggggggg fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
  fwwwwwwwwwwwwwwwwwwwwwww                                                       f
  fmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm                                             f
  ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
```

### 8.8 Picker

The picker is `vecnet tui --pick` (PR #37). It runs before the shell, so it has no header bar: only the brand chip, a selection fill on the chosen row, and `local` in `warn`.

```text
 vecnet  choose an operator profile

name             hub
> home             hub.example:8443
  fly-operator     vecnet.xk.io
  dev              127.0.0.1:8443 local

j/k move  enter choose  q cancel  click a row
Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
  bbbbbbbbmmmmmmmmmmmmmmmmmmmmmmmmmmm

  mmmmmmmmmmmmmmmmmmmm
S A#######################################################

                                    wwwww

  mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm
  mmmmmmmmmmmmmmmmmmmmmmm
```

The confirm step. The keys are `action`. Neither choice is a warning.

```text
 vecnet  confirm

run dev for this session
127.0.0.1:8443 local

make this the default operator_profile?
 y  make default    n  this session only   esc back

Shift+drag selects text
```

Paint (truecolor and 256; codes in section 6.1):

```text
  bbbbbbbbmmmmmmmm


  mmmmmmmmmmmmmm wwwww


  vvvmmmmmmmmmmmmmmmmvvvmmmmmmmmmmmmmmmmmmmmmmmmmmmmm

  mmmmmmmmmmmmmmmmmmmmmmm
```

## 9. Contrast evidence

These are WCAG contrast ratios for every foreground role, computed against reference terminal backgrounds:

- **Dark:** `#0b0f19` (vecnet), `#000000`, `#1e1e1e`, One Dark `#282c34` and Solarized Dark `#002b36`.
- **Light:** `#ffffff`, Solarized Light `#fdf6e3`, Catppuccin Latte `#eff1f5` and `#f5f5f5`.

The 256 columns use each index's fixed RGB value. Each cell is the range from the worst to the best background.

| Role | Dark value | on dark terminals | 256 dark | on dark terminals | Light value | on light terminals | 256 light | on light terminals |
|---|---|---|---|---|---|---|---|---|
| muted | `#9aa4b8` | 5.6–8.4 | 248 `#a8a8a8` | 5.9–8.8 | `#5f6a84` | 4.8–5.4 | 60 `#5f5f87` | 5.3–6.0 |
| dim | `#5f6a84` | 2.6–3.9 | 60 `#5f5f87` | 2.3–3.5 | `#8391ad` | 2.8–3.2 | 103 `#8787af` | 3.0–3.4 |
| accent | `#a855f7` | 3.5–5.3 | 135 `#af5fff` | 3.9–5.9 | `#7c3aed` | 5.0–5.7 | 92 `#8700d7` | 6.2–7.0 |
| indigo | `#818cf8` | 4.7–7.0 | 105 `#8787ff` | 4.6–6.9 | `#4f46e5` | 5.6–6.3 | 63 `#5f5fff` | 4.1–4.6 |
| action | `#a5b4fc` | 7.0–10.5 | 147 `#afafff` | 6.9–10.4 | `#4f46e5` | 5.6–6.3 | 63 `#5f5fff` | 4.1–4.6 |
| unread / warn | `#fbbf24` | 8.4–12.6 | 214 `#ffaf00` | 7.6–11.4 | `#b45309` | 4.4–5.0 | 130 `#af5f00` | 4.2–4.7 |
| success | `#34d399` | 7.3–10.9 | 43 `#00d7af` | 7.6–11.3 | `#047857` | 4.8–5.5 | 29 `#00875f` | 4.0–4.5 |
| error | `#fb7185` | 5.2–7.8 | 204 `#ff5f87` | 4.8–7.2 | `#be123c` | 5.6–6.3 | 161 `#d7005f` | 4.6–5.2 |
| info | `#38bdf8` | 6.5–9.8 | 74 `#5fafd7` | 5.7–8.6 | `#0369a1` | 5.2–5.9 | 24 `#005f87` | 6.2–7.0 |
| hold | `#8391ad` | 4.4–6.6 | 103 `#8787af` | 4.1–6.1 | `#64748b` | 4.2–4.8 | 67 `#5f87af` | 3.3–3.8 |
| rule | `#3d4a73` | 1.6–2.4 | 60 `#5f5f87` | 2.3–3.5 | `#b8c0d6` | 1.6–1.8 | 146 `#afafd7` | 1.9–2.1 |
| identity 1 | `#818cf8` | 4.7–7.0 | 105 `#8787ff` | 4.6–6.9 | `#4f46e5` | 5.6–6.3 | 63 `#5f5fff` | 4.1–4.6 |
| identity 2 | `#c084fc` | 5.3–7.9 | 141 `#af87ff` | 5.2–7.7 | `#9333ea` | 4.8–5.4 | 92 `#8700d7` | 6.2–7.0 |
| identity 3 | `#e879f9` | 5.7–8.5 | 213 `#ff87ff` | 6.8–10.2 | `#c026d3` | 4.2–4.7 | 164 `#d700d7` | 3.8–4.3 |
| identity 4 | `#22d3ee` | 7.7–11.6 | 45 `#00d7ff` | 8.1–12.2 | `#0e7490` | 4.7–5.4 | 31 `#0087af` | 3.7–4.1 |
| identity 5 | `#60a5fa` | 5.5–8.3 | 75 `#5fafff` | 6.0–9.1 | `#2563eb` | 4.6–5.2 | 26 `#005fd7` | 5.1–5.8 |

How to read the table:

- Every role that carries meaning reaches at least 3.3:1 on every reference background, in every rung and set. Most reach 4.5:1 or more.
- `accent` in truecolor is weakest on One Dark (3.5:1). It is used for bars, borders and bold titles, where 3:1 is enough.
- `dim` (2.3–3.9:1) is only for ids and placeholders, which never carry meaning on their own.
- `rule` (1.6–3.5:1) is meant to be quiet. It only has to be visible.

Fills, checked against their own foregrounds and against the terminal backgrounds (ΔE76 is the colour distance):

- **Selection, dark:** `#2c3756` with `#f3f4f6` text gives 10.7:1. Error on it is 4.4:1 and amber 7.0:1. It stands ΔE76 16–31 from the dark references.
- **Selection, light:** `#c7d2fe` with `#111827` text gives 11.9:1. It stands ΔE76 24–35 from the light references.
- **Selection, 256 dark:** index 17 (`#00005f`) with 255 text gives about 16:1, error 6.2:1, amber 9.8:1.
- **Header bar:** `#1e2640` with `#f3f4f6` text gives 13.6:1 and muted 6.0:1. The bar is meant to be subtle (ΔE76 13–24), since the brand chip and the active tab mark it out.
- **Brand chip:** white on `#6366f1` gives 4.5:1, and on `#a855f7` 4.0:1, bold.

## 10. Integrator plan and acceptance

### 10.1 Package shape (guidance)

`internal/tui/theme` should contain:

- `Detect(env []string, out *os.File) Profile`, following the section 4.1 rule.
- `New(p Profile, dark bool) *Theme`.
- Role accessors that return `lipgloss.Style`:
  - `Fg(Role)` and `Fill(Fill)`;
  - `Sender(peerID)` and `Kind(kind)`;
  - state helpers `Priority`, `Receipt`, `Presence`, `Reach`, `Health`, `EventType`, `Quota` and `Conn`, each implementing one row of section 5.5.
- `Pane(...)` border styles for focused and unfocused panes.

How it fits into the TUI:

- The `Model` holds the `*Theme`. A `tea.BackgroundColorMsg` swaps the dark or light set, and the frame redraws.
- The picker builds its own `Theme` from the same `Detect`.
- Every colour in `theme.go`, `screen_chat.go` (`colorCyan`, `colorPick`, `styleAccepted`, `styleSender`), `screen_dash.go` (`styleDash*`, `colorDashCursor`) and `screen_inbox.go` (`inboxSelBg`) is deleted.

### 10.2 Tests

1. **Profile detection, table-driven.** Cases:
   - `NO_COLOR` set to `1`, `yes`, `0` and the empty string (only the empty string keeps colour);
   - `COLORTERM` set to `truecolor`, `24bit`, `TrueColor`, `yes` and unset;
   - `TERM` set to `xterm-256color`, `xterm`, `screen` and `dumb`.
2. **Every role resolves** on every rung, in both sets.
3. **No colour SGR under `NO_COLOR`.** Render every screen with `NO_COLOR` and assert that no SGR parameter is in 30–49 or 90–107 and none starts with `38;` or `48;`. Also assert that bold and reverse do appear where sections 6.4 and 6.5 require them: on the selected row, the focused pane's title and unread counts.
4. **No 256 SGR touches 0–15**, and in the 16-colour rung no foreground is outside 31–36.
5. **No frame background:** `View().BackgroundColor` and `View().ForegroundColor` are nil.
6. **The hostile-text cases** from section 6.8.
7. **Plain-text goldens** change only for heavy borders and the inbox `>`. Regenerate them with `UPDATE_GOLDEN=1` and check that the diff contains nothing else.

### 10.3 ui-shots (C5, required before merge)

The harness has to grow first:

- `scripts/ui/tmux.mjs` needs a colour-mode knob, which today is fixed at `COLORTERM=truecolor`, `TERM=xterm-256color` and `NO_COLOR` unset. The modes:
  - truecolor (the default);
  - 256: `TERM=xterm-256color` with no `COLORTERM`;
  - 16: `TERM=xterm` with no `COLORTERM`;
  - none: `NO_COLOR=1`.
- It also needs a `VECNET_TUI_BG` knob.
- `scripts/ui/ansi.mjs` needs a light page background for light shots. It already renders faint as reduced opacity.

The shots:

- **Default theme (truecolor, dark), every TUI screen:**
  - inbox (list, detail, event log), including an approval and a DLQ row;
  - chat with the sidebar, a selected message and both thread groups;
  - dash;
  - subs;
  - the command line in NORMAL, INSERT and COMMAND, with an ok toast and an error toast;
  - prefs, in its normal and error states;
  - the picker and its confirm step;
  - help;
  - the header with a notice row and with a `local` hub.

  The existing `17`–`22`, `att05`, `nt08`, `rx07` and `ln04` TUI shots are regenerated as part of this.
- **`NO_COLOR`:** chat and inbox at least.
- **16 colours:** chat and inbox at least.
- **Recommended:** light-terminal shots (`VECNET_TUI_BG=light` on a light page) of chat and inbox, and a 256 shot of chat.

CoS reviews these shots by eye before merging.

### 10.4 Stacking

The integrator branches from PR #37 (or from master once #37 has merged), after CoS accepts this design. It must not change web static files, the hub, the store or migrations.

## 11. Decisions CoS may veto

This design makes these choices. Each one is cheap to reverse before the integrator starts.

1. **Heavy borders for the focused pane** (section 6.4). The alternative is a bold title only, which is weaker under `NO_COLOR` and in 16 colours.
2. **`VECNET_TUI_BG`** as the escape hatch when a terminal does not answer the background query, and for light ui-shots.
3. **A brand-chip gradient in truecolor only**: eight cells interpolated from `#6366f1` to `#a855f7`. The 256 and 16 rungs use a solid colour.
4. **Mode chip colours:** NORMAL indigo, INSERT violet, COMMAND sky. Amber and red stay reserved.
5. **The light set is used only when the terminal reports a light background.** When the answer is unknown the dark set is used, rather than a third set designed to work on both.
