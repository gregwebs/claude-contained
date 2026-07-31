#!/bin/bash
# Redirect Chrome invocations to the Playwright-managed Chromium binary.
exec /ms-playwright/chromium-*/chrome-linux/chrome --no-sandbox "$@"
