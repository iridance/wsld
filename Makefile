SHELL := /bin/bash
export PATH := $(HOME)/.local/bin:$(HOME)/.local/go/bin:$(PATH)

.PHONY: test test-scripts test-scripts-bash test-scripts-ps lint-scripts

test: test-scripts

test-scripts: lint-scripts test-scripts-bash test-scripts-ps

lint-scripts:
	shellcheck -x -P SCRIPTDIR scripts/02-bootstrap-docker.sh scripts/lib/bootstrap-lib.sh

test-scripts-bash:
	bats scripts/tests

# 透過 WSL interop 呼叫 Windows PowerShell 5.1 內建的 Pester 3.x
test-scripts-ps:
	@if command -v powershell.exe >/dev/null 2>&1; then \
	  win="$$(wslpath -w "$$PWD/scripts/tests")"; \
	  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command \
	    "Import-Module Pester; \$$r = Invoke-Pester -Path '$$win' -PassThru -Quiet; \$$r.TestResult | Where-Object { -not \$$_.Passed } | ForEach-Object { 'FAIL: ' + \$$_.Describe + ' / ' + \$$_.Name + ' :: ' + \$$_.FailureMessage }; Write-Output ('Pester PASSED=' + \$$r.PassedCount + ' FAILED=' + \$$r.FailedCount); if (\$$r.FailedCount -gt 0) { exit 1 }" \
	    | tr -d '\r'; \
	else echo "skip Pester (powershell.exe not found)"; fi
