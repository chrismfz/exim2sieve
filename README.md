# exim2sieve

Exim2Sieve is a utility to migrate and convert cPanel Exim filters to Sieve scripts for modern mail servers such as Mailcow, DirectAdmin, or other Sieve-compatible systems.

It is written in Go with a modular architecture, allowing easy extensions to support multiple mail server backends.

## Features

- Export cPanel Exim filters for a user or all accounts.
- Convert cPanel filter rules (simple rules) into Sieve scripts.
- Import converted Sieve scripts into Mailcow, DirectAdmin, or custom targets.
- Modular backend support (expandable via packages).
- CLI interface with both interactive and non-interactive modes.

## Installation

```bash
git clone https://github.com/chrismfz/exim2sieve.git
cd exim2sieve
go build -o exim2sieve
```

## Usage

```bash
# Export filters for a specific cPanel account
exim2sieve --export --account username-here

# Import previously converted filters into Mailcow
exim2sieve --import --account username-here --target mailcow

# Interactive mode
exim2sieve --interactive
```

### CLI Options

- `--export` : Export cPanel Exim filters to internal format.
- `--import` : Import Sieve scripts into the target mail server.
- `--account` : Specify the user account.
- `--target` : Target server type (`mailcow`, `directadmin`, `custom`).
- `--interactive` : Interactive CLI mode for step-by-step operation.

## Extending Backends

The project is designed to support multiple backends via modular packages:

- `cpanel` : Read/export cPanel Exim filters.
- `sieve` : Convert internal format to Sieve.
- `mailcow`, `directadmin`, `custom` : Import modules for each target.

Adding a new backend only requires implementing the interface defined in the `backends` package.

## Example Flow

1. Export filters from cPanel:

```bash
exim2sieve --export --account myipgr
```

2. Convert to Sieve (internal format automatically converted):

```bash
exim2sieve --convert --account myipgr
```

3. Import to target:

```bash
exim2sieve --import --account myipgr --target mailcow
```

## Contributing

Contributions are welcome. Please fork the repository, implement features or bug fixes, and open a pull request.

## License

MIT License

