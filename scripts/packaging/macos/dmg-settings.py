# dmgbuild settings for both dmgs (scripts/build-macos-dmgs.sh passes -D app=<path to the .app>
# -D background=<background.png>; a background@2x.png beside it makes the Retina version).
# One window: the app on the left, an arrow, the Applications link on the right.
import os.path

app = defines["app"]  # noqa: F821 (dmgbuild provides defines)
name = os.path.basename(app)

format = "ULFO"  # lzfse, macOS 10.11+
filesystem = "HFS+"
files = [app]
symlinks = {"Applications": "/Applications"}
# No hide_extensions: it sets com.apple.FinderInfo on the app, which breaks codesign --strict
# ("Finder information … not allowed"). Finder hides .app by default anyway.
icon = os.path.join(app, "Contents", "Resources", "AppIcon.icns")

background = defines["background"]  # noqa: F821
window_rect = ((200, 120), (640, 400))
default_view = "icon-view"
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
icon_size = 128
text_size = 13
icon_locations = {name: (160, 190), "Applications": (480, 190)}
