; Shared by server.iss and agent.iss (01-no-cli-plan item 2.3): the definitions the build passes
; in. Their shared [Code] helpers are common-code.iss, included at the top of each [Code] section.
; Built by scripts/build-windows-installers.sh.

#ifndef AppVersion
  #error Define AppVersion (the release tag without its "v", e.g. 0.1.0-rc1)
#endif
#ifndef NumericVersion
  #error Define NumericVersion (a.b.c.d, the file version Windows shows)
#endif
#ifndef BinDir
  #error Define BinDir (the directory holding the programs to package)
#endif
#ifndef OutputDir
  #error Define OutputDir
#endif

#define Publisher "Jarvis Automation"
#define ProjectURL "https://github.com/alexberardi/jarvis-server"
#define UninstallKeyRoot "Software\Microsoft\Windows\CurrentVersion\Uninstall\"
