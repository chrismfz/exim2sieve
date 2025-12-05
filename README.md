# exim2sieve

`exim2sieve` is a small Go utility that helps you **migrate cPanel Exim filters and mailboxes** to modern, Sieve‑capable mail platforms such as **Mailcow**, **DirectAdmin**, or any other Dovecot/Sieve setup.

It focuses on:

- Exporting cPanel filter definitions (`filter.yaml` / `filter`) per mailbox.
- Converting those filters to Sieve scripts with sane names and comments.
- Importing Sieve scripts into the target server via `doveadm`.
- Optionally exporting & importing full **Maildir** contents.
- Creating Mailcow mailboxes via API based on the exported backup tree.

---

## High‑level workflow

Typical migration flow from cPanel → Mailcow / other Dovecot:

1. **Export** filters (and optionally maildirs) from the cPanel server:
   ```bash
   ./exim2sieve -cpanel-user myipgr -dest ./backup -maildir
   ```
2. **(Optional)** Create mailboxes in Mailcow based on the export tree:
   ```bash
   ./exim2sieve -config exim2sieve.conf      -create-mailcow-mailboxes      -backup ./backup/myipgr      -domain myip.gr
   ```
3. **Import Sieve** into the target Dovecot:
   ```bash
   ./exim2sieve -config exim2sieve.conf      -import-sieve      -backup ./backup/myipgr      -domain myip.gr
   ```
4. **Import Maildir** contents (message migration):
   ```bash
   ./exim2sieve -config exim2sieve.conf      -import-maildir      -backup ./backup/myipgr      -domain myip.gr
   ```

You can also work per single mailbox using `-mailbox chris` or `-mailbox chris@myip.gr`.

---

## Features (current)

### 1. cPanel export

- Export all filters for a **single cPanel account**:
  - Per‑domain `_domain.filter` + `_domain.sieve`.
  - Per‑mailbox `filter.yaml` / `filter` + converted `*.sieve`.
- Optional **Maildir export** for each mailbox.

Command:

```bash
./exim2sieve -cpanel-user myipgr -dest ./backup
./exim2sieve -cpanel-user myipgr -dest ./backup -maildir   # include Maildir data
```

Export layout:

```text
backup/
  myipgr/                     ← cPanel user
    mailcow_mailboxes.log     ← optional log from Mailcow creation step
    myip.gr/                  ← domain
      _domain.filter          ← raw /etc/vfilters/myip.gr (optional)
      _domain.sieve           ← converted domain-wide Sieve (if present)
      chris/
        filter.yaml           ← original cPanel YAML filter
        chris.sieve           ← combined Sieve for this mailbox
        maildir/              ← optional Maildir copy (if -maildir used)
      admin/
        filter.yaml
        admin.sieve
        maildir/
      ...
```

### 2. Filter parsing & Sieve conversion

Supported inputs:

- `filter.yaml` (cPanel YAML filter format).
- Text `filter` files (cPanel Exim filter syntax).

The internal `sieve` package:

- Parses cPanel rules into a neutral `Filter` format.
- Converts each **enabled** filter entry into a `SieveScript`.
- Combines multiple rules into a **single, clean Sieve script** per mailbox using `CombineScripts`.

#### Names & comments

- Each Exim filter rule has a `filtername` (from YAML) or `#Name` (from text file).
- The combined Sieve script:
  - Deduplicates all `require [...]` lines and puts them at the top.
  - For each rule, adds:
    ```sieve
    # Filter: Nixpal
    if address :is "from" "support@nixpal.com" {
        fileinto "Nixpal";
        stop;
    }
    ```
- Roundcube / Sieve UIs often encode rule names as:
  ```sieve
  # rule:[Nixpal]
  ```
  The converter already preserves human‑readable block labels via `# Filter: ...`.  
  You can later extend it to emit `# rule:[name]` if you want Roundcube‑style naming.

Example YAML → Sieve:

```yaml
filter:
  -
    enabled: 1
    filtername: Nixpal
    rules:
      - part: "$header_from:"
        match: is
        opt: or
        val: support@nixpal.com
    actions:
      - action: save
        dest: Nixpal
      - action: finish
        dest: ~
```

Becomes:

```sieve
require ["fileinto"];

# Filter: Nixpal
if address :is "from" "support@nixpal.com"
{
    fileinto "Nixpal";
    stop;
}
```

### 3. Single file conversion mode (`-path`)

You can convert a *single* `filter.yaml` or `filter` file to Sieve as a quick test or standalone tool:

```bash
./exim2sieve -path /home/myipgr/etc/myip.gr/chris/filter.yaml -dest ./out
```

The tool auto‑detects YAML vs text:

- If it looks like `filter.yaml` → parse YAML.
- Otherwise → parse text Exim filter.

Output:

```text
out/
  filters.sieve   ← combined Sieve script for that YAML/filter file
```

---

## 4. Sieve import via `doveadm` (`-import-sieve`)

After you have a backup tree (e.g. `./backup/myipgr`), you can import Sieve into the target Dovecot using `doveadm`.

### Config (`exim2sieve.conf`)

Basic example:

```ini
[doveadm]
# Bare metal dovecot:
command = doveadm

# Mailcow (dovecot in Docker), e.g.:
# command = docker exec -i mailcowdockerized-dovecot-mailcow-1 doveadm

[mailcow]
# Optional, used only by -create-mailcow-mailboxes mode
api_url = https://mail.example.com
api_key = your-mailcow-api-key
default_quota = 5GB

[paths]
# Optional, for Maildir import mapping (see below)
# maildir_host_base = /root/chris/backup
# maildir_container_base = /backup
```

### Import all Sieve scripts for a domain

```bash
./exim2sieve -config exim2sieve.conf   -import-sieve   -backup ./backup/myipgr   -domain myip.gr
```

What it does:

- Walks `./backup/myipgr/myip.gr/<localpart>/`.
- For each mailbox (e.g. `chris`):
  - Finds `chris.sieve` (or first `*.sieve`).
  - Checks if user exists on the target:
    ```bash
    doveadm user -u chris@myip.gr
    ```
  - If the user exists:
    - Runs:
      ```bash
      doveadm sieve put -u chris@myip.gr cpanel-migrated < chris.sieve
      doveadm sieve activate -u chris@myip.gr cpanel-migrated
      ```
    - Logs:
      ```text
      Imported sieve for chris@myip.gr from .../chris.sieve as cpanel-migrated
      ```

### Import Sieve for a single mailbox

Use the `-mailbox` filter:

```bash
# by localpart
./exim2sieve -config exim2sieve.conf   -import-sieve   -backup ./backup/myipgr   -domain myip.gr   -mailbox chris

# or full address
./exim2sieve -config exim2sieve.conf   -import-sieve   -backup ./backup/myipgr   -domain myip.gr   -mailbox chris@myip.gr
```

The importer will match either `uname == "chris"` or address `chris@myip.gr`.

---

## 5. Maildir export (`-maildir`) & import (`-import-maildir`)

### Export side (on cPanel)

When you export with `-maildir`, `ExportUser` additionally copies each mailbox’s Maildir:

```bash
./exim2sieve -cpanel-user myipgr -dest ./backup -maildir
```

Layout example:

```text
backup/myipgr/myip.gr/chris/
  filter.yaml
  chris.sieve
  maildir/
    cur/
    new/
    tmp/
backup/myipgr/myip.gr/admin/
  filter.yaml
  admin.sieve
  maildir/
    ...
```

The Maildir copy is a straightforward directory copy (retaining `cur/`, `new/`, `tmp/`).

### Import side (on destination Dovecot)

Use `-import-maildir`:

```bash
./exim2sieve -config exim2sieve.conf   -import-maildir   -backup ./backup/myipgr   -domain myip.gr
```

For each mailbox:

- Verifies that `maildir/` exists and looks like Maildir (at least `cur/` or `new/`).
- Checks if the user exists via `doveadm user -u addr`.
- If yes, runs:
  ```bash
  doveadm import -u addr maildir:/path/to/maildir "" ALL
  ```
- Logs:

  ```text
  Imported maildir for chris@myip.gr from backup/myipgr/myip.gr/chris/maildir
  ```

### Docker‑safe Maildir path mapping

When Dovecot runs inside Docker (e.g. Mailcow), the **host path** where backups live may not be visible directly.  
To handle this, `ImportMaildir` supports **host→container mapping** via `[paths]` config:

```ini
[paths]
maildir_host_base = /root/chris/backup
maildir_container_base = /backup
```

Assuming you mount your backup into the Dovecot container:

```yaml
# docker-compose override example
dovecot-mailcow:
  volumes:
    - /root/chris/backup:/backup:ro
```

Then host path:

```text
/root/chris/backup/myipgr/myip.gr/chris/maildir
```

is automatically mapped to:

```text
/backup/myipgr/myip.gr/chris/maildir
```

So the actual `doveadm` command becomes:

```bash
doveadm import -u chris@myip.gr maildir:/backup/myipgr/myip.gr/chris/maildir "" ALL
```

If `maildir_host_base` / `maildir_container_base` are **empty**, the tool simply uses the host path as‑is (good for DirectAdmin / bare metal Dovecot).

You can also limit to a single mailbox:

```bash
./exim2sieve -config exim2sieve.conf   -import-maildir   -backup ./backup/myipgr   -domain myip.gr   -mailbox chris
```

---

## 6. Mailcow mailbox creation (`-create-mailcow-mailboxes`)

`exim2sieve` can read the backup tree and create Mailcow mailboxes via API before you import filters/Maildir.

### Config

In `exim2sieve.conf`:

```ini
[mailcow]
api_url = https://mail.example.com
api_key = your-mailcow-api-key
default_quota = 10GB   ; mailbox quota (MB/GB syntax supported)
```

### Command

```bash
./exim2sieve -config exim2sieve.conf   -create-mailcow-mailboxes   -backup ./backup/myipgr   -domain myip.gr
```

Behavior:

- Walks `./backup/myipgr/myip.gr/<localpart>/` and builds a mailbox list.
- Uses the Mailcow API client to:
  - Ensure the domain exists (or create it if needed, depending on your Mailcow config/permissions).
  - Create each mailbox with the configured default quota.
- Logs a summary to `mailcow_mailboxes.log` under `backup/myipgr`.

You can then run `-import-sieve` and `-import-maildir` to attach filters and messages to those mailboxes.

---

## 7. CLI reference (current flags)

`exim2sieve` is **mode‑based**: exactly one of the main modes should be active per run.

### Main modes

- `-cpanel-user <user>` / `-account <user>`  
  Export filters (and optionally Maildir) for a cPanel account:
  ```bash
  ./exim2sieve -cpanel-user myipgr -dest ./backup
  ./exim2sieve -cpanel-user myipgr -dest ./backup -maildir
  ```

- `-path <file>`  
  Convert a single `filter.yaml` or `filter` to `filters.sieve` in `-dest`.

- `-import-sieve`  
  Import Sieve scripts from a backup using `doveadm`.

- `-import-maildir`  
  Import Maildir contents from a backup using `doveadm import`.

- `-create-mailcow-mailboxes`  
  Create Mailcow domains/mailboxes from a backup tree via the Mailcow API.

### Common flags

- `-dest <dir>`  
  Destination folder for exports (default: `./backup`).

- `-maildir`  
  When exporting from cPanel, also copy each mailbox's Maildir under `maildir/`.

- `-backup <path>`  
  Root of existing backup tree for import modes (e.g. `./backup/myipgr`).

- `-domain <domain>`  
  Limit import/export to a specific domain (`myip.gr`).  
  (Useful when an account has many domains.)

- `-mailbox <name or full address>`  
  Limit Sieve/Maildir import to a single mailbox:
  - `-mailbox chris`
  - `-mailbox chris@myip.gr`

- `-config <file>`  
  Path to `exim2sieve.conf`.  
  If omitted, the loader will try `./exim2sieve.conf` and `/etc/exim2sieve.conf`.  
  Fallback defaults: `doveadm` in PATH, Mailcow disabled.

---

## Example scenarios

### A. Full migration from cPanel → Mailcow (single account / domain)

On the **cPanel** server:

```bash
./exim2sieve -cpanel-user myipgr -dest /root/chris/backup -maildir
tar czf myipgr-backup.tgz -C /root/chris/backup myipgr
```

Move `myipgr-backup.tgz` to the **Mailcow** host, extract to `/root/chris/backup`.

On the **Mailcow** host:

1. Ensure backup is mounted inside Dovecot container:

   ```yaml
   dovecot-mailcow:
     volumes:
       - /root/chris/backup:/backup:ro
   ```

2. Configure `exim2sieve.conf`:

   ```ini
   [doveadm]
   command = docker exec -i mailcowdockerized-dovecot-mailcow-1 doveadm

   [mailcow]
   api_url = https://mail.example.com
   api_key = your-mailcow-api-key
   default_quota = 10GB

   [paths]
   maildir_host_base = /root/chris/backup
   maildir_container_base = /backup
   ```

3. Create Mailcow mailboxes:

   ```bash
   ./exim2sieve -config exim2sieve.conf      -create-mailcow-mailboxes      -backup /root/chris/backup/myipgr      -domain myip.gr
   ```

4. Import Sieve:

   ```bash
   ./exim2sieve -config exim2sieve.conf      -import-sieve      -backup /root/chris/backup/myipgr      -domain myip.gr
   ```

5. Import Maildir:

   ```bash
   ./exim2sieve -config exim2sieve.conf      -import-maildir      -backup /root/chris/backup/myipgr      -domain myip.gr
   ```

### B. Partial test migration (single mailbox, filters‑only)

```bash
./exim2sieve -cpanel-user myipgr -dest ./backup      # on cPanel side
rsync -av ./backup/myipgr mycow:/srv/exim2sieve/     # copy tree

# On target server (with dovecot):
./exim2sieve -config exim2sieve.conf   -import-sieve   -backup /srv/exim2sieve/myipgr   -domain myip.gr   -mailbox chris
```

---

## Contributing

Contributions, bug reports and feature ideas are welcome.

- Add support for more complex Exim constructs.
- Improve Sieve rule naming (e.g. Roundcube‑style `# rule:[name]`).
- Extend import/export helpers for other control panels.

Please open an issue or PR on GitHub with a clear description and, if possible, sample `filter.yaml` / `filter` snippets.

---


Usage flow

1. Export from cPanel (creates backup + shadow under per-domain dirs):
./exim2sieve -cpanel-user mycpuser -dest ./backup -maildir
# → ./backup/mycpuser/<domain>/shadow

2. Create mailboxes in Mailcow via API:
./exim2sieve -config exim2sieve.conf \
  -create-mailcow-mailboxes \
  -backup ./backup/mycpuser \
  -domain myip.gr

3. Patch passwords from shadow into Mailcow MySQL:
./exim2sieve -config exim2sieve.conf \
  -mailcow-passwords-from-shadow \
  -backup ./backup/mycpuser \
  -domain myip.gr

