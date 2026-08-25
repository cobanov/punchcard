# punchcard menu bar app — build, package, sign, notarize
#
#   make            build
#   make test       tests
#   make bundle     PunchcardBar.app (unsigned)
#   make sign       sign with Developer ID
#   make notarize   submit to Apple and staple
#   make install    build, sign, install into /Applications and launch
#   make clean
#
# This is a separate Makefile on purpose. The Swift toolchain has nothing to do
# with the Go gate at the repo root, and `make check` there must not depend on
# a machine having Xcode.

VERSION     := 0.1.0
APP         := PunchcardBar
BUNDLE_ID   := run.cobanov.punchcard.bar
MIN_MACOS   := 14.0

BUILD_DIR   := build
APP_BUNDLE  := $(BUILD_DIR)/$(APP).app
CONTENTS    := $(APP_BUNDLE)/Contents
BINARY      := .build/release/$(APP)
ICONSET     := $(BUILD_DIR)/$(APP).iconset
ICNS        := $(BUILD_DIR)/$(APP).icns
ZIP         := $(BUILD_DIR)/punchcard-bar-$(VERSION)-macos.zip

# Listed by: security find-identity -v -p codesigning
SIGN_ID     := Developer ID Application: AHMET MERT COBANOGLU (6U58AKY6F8)
# Stored by: xcrun notarytool store-credentials
NOTARY_PROFILE := ghbar

# The signing key lives in a keychain of its own, not the login keychain.
#
# codesign against the login keychain fails with errSecInternalComponent in any
# non-interactive session — the login keychain will not unlock without a person
# at the machine. A dedicated keychain, created once with the p12 and given a
# partition list that names codesign, signs without a prompt. The setup and its
# password live in ~/.ghbar-signing (shared with GHBar; back that directory up,
# because losing the private key means requesting the certificate again).
SIGN_KEYCHAIN := $(HOME)/Library/Keychains/ghbar-signing.keychain-db
KC_PASS_FILE  := $(HOME)/.ghbar-signing/.kcpass

.PHONY: all build test run icon bundle sign unlock notarize zip install clean

all: build

build:
	swift build -c release

test:
	swift test

run:
	swift run

# --- Icon ---------------------------------------------------------------

icon: $(ICNS)

$(ICNS):
	@mkdir -p $(BUILD_DIR)
	swift Tools/makeicon.swift $(ICONSET)
	iconutil -c icns $(ICONSET) -o $(ICNS)

# --- Bundle -------------------------------------------------------------

bundle: build $(ICNS)
	@rm -rf $(APP_BUNDLE)
	@mkdir -p $(CONTENTS)/MacOS $(CONTENTS)/Resources
	cp $(BINARY) $(CONTENTS)/MacOS/$(APP)
	cp $(ICNS) $(CONTENTS)/Resources/$(APP).icns
	@printf '%s' 'APPL????' > $(CONTENTS)/PkgInfo
	@# Two calls: PlistBuddy on this macOS aborts past 13 -c commands
	@# (measured: 13 fine, 14+ Abort trap 6).
	@/usr/libexec/PlistBuddy -c "Clear dict" \
	  -c "Add :CFBundleName string punchcard" \
	  -c "Add :CFBundleDisplayName string punchcard" \
	  -c "Add :CFBundleIdentifier string $(BUNDLE_ID)" \
	  -c "Add :CFBundleExecutable string $(APP)" \
	  -c "Add :CFBundleIconFile string $(APP)" \
	  -c "Add :CFBundlePackageType string APPL" \
	  -c "Add :CFBundleShortVersionString string $(VERSION)" \
	  -c "Add :CFBundleVersion string $(VERSION)" \
	  $(CONTENTS)/Info.plist >/dev/null
	@# LSUIElement=true keeps it out of the Dock and the app switcher, which is
	@# what makes it a menu bar app rather than an app with a menu bar item.
	@/usr/libexec/PlistBuddy \
	  -c "Add :LSMinimumSystemVersion string $(MIN_MACOS)" \
	  -c "Add :LSUIElement bool true" \
	  -c "Add :LSApplicationCategoryType string public.app-category.developer-tools" \
	  -c "Add :ITSAppUsesNonExemptEncryption bool false" \
	  -c "Add :NSHumanReadableCopyright string 'Copyright © 2026 Mert Cobanov. MIT License.'" \
	  $(CONTENTS)/Info.plist >/dev/null
	@echo "bundled: $(APP_BUNDLE)"

# --- Signing ------------------------------------------------------------
# --options runtime (hardened runtime) is required for notarization.
# --timestamp adds a trusted timestamp, so old signatures stay valid after the
# certificate expires.

sign: bundle unlock
	codesign --force --options runtime --timestamp --keychain $(SIGN_KEYCHAIN) \
	  --sign "$(SIGN_ID)" $(CONTENTS)/MacOS/$(APP)
	codesign --force --options runtime --timestamp --keychain $(SIGN_KEYCHAIN) \
	  --sign "$(SIGN_ID)" $(APP_BUNDLE)
	@echo "--- verify ---"
	codesign --verify --deep --strict --verbose=2 $(APP_BUNDLE)

unlock:
	@test -f $(KC_PASS_FILE) || { echo "missing $(KC_PASS_FILE) — see the comment above SIGN_KEYCHAIN"; exit 1; }
	@security unlock-keychain -p "$$(cat $(KC_PASS_FILE))" $(SIGN_KEYCHAIN)

# --- Notarization -------------------------------------------------------
# Cannot be run from an agent session: the notarytool profile lives in the
# LOGIN keychain, which does not unlock without a person at the machine. Run
# this yourself from a terminal.
# Submitted to Apple, scanned, then the ticket is stapled to the app so a
# user's machine can verify it while offline.

notarize: sign zip
	xcrun notarytool submit $(ZIP) --keychain-profile "$(NOTARY_PROFILE)" --wait
	xcrun stapler staple $(APP_BUNDLE)
	@rm -f $(ZIP)
	$(MAKE) zip
	@echo "--- Gatekeeper ---"
	spctl --assess --type execute --verbose=4 $(APP_BUNDLE)

zip:
	@mkdir -p $(BUILD_DIR)
	ditto -c -k --keepParent $(APP_BUNDLE) $(ZIP)
	@echo "archived: $(ZIP)"

# --- Install ------------------------------------------------------------

# Unsigned on purpose: a locally built app runs fine on the machine that built
# it, and requiring a signature to try your own work is friction for nothing.
# Signing is for handing the app to someone else — `make sign` or `make notarize`.
install: bundle
	@pkill -x $(APP) || true
	@rm -rf /Applications/punchcard.app
	cp -R $(APP_BUNDLE) /Applications/punchcard.app
	open /Applications/punchcard.app
	@echo "installed and launched"

clean:
	rm -rf $(BUILD_DIR) .build
