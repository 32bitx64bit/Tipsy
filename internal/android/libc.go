// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

// bionicAliases maps bionic libc names onto glibc symbols of the same ABI.
var bionicAliases = map[string]string{
	"__errno": "__errno_location",
}

// libcSymbols is a table of names from the official libroblox.so libc import
// set (baseline 2.734.917) plus common NDK/bionic companions. Lookup still
// tries dlsym(RTLD_DEFAULT) for any requested name; this list documents the
// expected surface and is used by tests.
var libcSymbols = []string{
	// memory / strings
	"memcpy", "memmove", "memset", "memcmp", "memchr", "memrchr",
	"strcpy", "strncpy", "strcat", "strncat", "strcmp", "strncmp",
	"strcasecmp", "strncasecmp", "strlen", "strnlen", "strchr", "strrchr",
	"strstr", "strpbrk", "strspn", "strcspn", "strtok_r", "strdup", "strndup",
	"strerror", "strerror_r", "strtol", "strtoul", "strtoll", "strtoull",
	"strtod", "strtof", "atoi", "atol", "atoll", "atof",
	"malloc", "calloc", "realloc", "free", "memalign", "posix_memalign",
	"malloc_usable_size", "mallinfo", "valloc",
	// FORTIFY
	"__memcpy_chk", "__memmove_chk", "__memset_chk", "__strcpy_chk",
	"__strncpy_chk", "__strcat_chk", "__strncat_chk", "__sprintf_chk",
	"__snprintf_chk", "__vsprintf_chk", "__vsnprintf_chk", "__printf_chk",
	"__fprintf_chk", "__vprintf_chk", "__vfprintf_chk", "__fread_chk",
	"__fwrite_chk", "__read_chk", "__recv_chk", "__recvfrom_chk",
	"__realpath_chk", "__getcwd_chk", "__fgets_chk", "__syslog_chk",
	"__stack_chk_fail", "__stack_chk_guard",
	"__strlen_chk", "__strchr_chk",
	"__strncpy_chk2", "__FD_SET_chk", "__FD_CLR_chk", "__FD_ISSET_chk",
	"__FD_ZERO_chk", "__assert2", "__gnu_strerror_r", "__sF",
	"__write_chk", "__sendto_chk", "__open_2", "__openat_2", "__poll_chk",
	"__readlink_chk", "__system_property_get",
	// errno / TLS / aux
	"__errno", "__errno_location", "gettid", "getauxval", "prctl", "ptrace",
	"arc4random_buf", "arc4random", "getpid", "getppid", "getuid", "geteuid",
	"getgid", "getegid", "geteuid", "getpagesize", "uname",
	"android_set_abort_message", "__cxa_thread_atexit_impl", "__register_atfork",
	// sysconf is tipsy_sysconf (bionic _SC_* numbers, not glibc)
	"environ", "__environ",
	// pthread
	"pthread_create", "pthread_join", "pthread_detach", "pthread_exit",
	"pthread_self", "pthread_equal", "pthread_once",
	"pthread_mutex_init", "pthread_mutex_destroy", "pthread_mutex_lock",
	"pthread_mutex_trylock", "pthread_mutex_unlock", "pthread_mutex_timedlock",
	"pthread_mutexattr_init", "pthread_mutexattr_destroy", "pthread_mutexattr_settype",
	"pthread_cond_init", "pthread_cond_destroy", "pthread_cond_wait",
	"pthread_cond_timedwait", "pthread_cond_signal", "pthread_cond_broadcast",
	"pthread_condattr_init", "pthread_condattr_destroy", "pthread_condattr_setclock",
	"pthread_rwlock_init", "pthread_rwlock_destroy", "pthread_rwlock_rdlock",
	"pthread_rwlock_wrlock", "pthread_rwlock_unlock", "pthread_rwlock_tryrdlock",
	"pthread_rwlock_trywrlock",
	"pthread_key_create", "pthread_key_delete", "pthread_getspecific", "pthread_setspecific",
	"pthread_attr_init", "pthread_attr_destroy", "pthread_attr_setdetachstate",
	"pthread_attr_setstacksize", "pthread_attr_getstack", "pthread_attr_setstack",
	"pthread_getattr_np", "pthread_setname_np", "pthread_getname_np",
	"pthread_kill", "pthread_sigmask", "pthread_setaffinity_np",
	"pthread_attr_setschedparam", "pthread_getschedparam", "pthread_setschedparam",
	// mmap / files
	"mmap", "munmap", "mprotect", "madvise", "msync", "mincore", "mlock", "munlock", "mremap",
	"open", "openat", "close", "read", "write", "pread", "pwrite", "pread64", "pwrite64",
	"readv", "writev", "lseek", "lseek64", "dup", "dup2", "dup3", "pipe", "pipe2",
	"fcntl", "ioctl", "fsync", "fdatasync", "ftruncate", "ftruncate64",
	"stat", "fstat", "lstat", "fstatat", "stat64", "fstat64",
	"mkdir", "mkdirat", "rmdir", "unlink", "unlinkat", "rename", "renameat",
	"chmod", "fchmod", "fchmodat", "fchown", "access", "faccessat", "getcwd", "chdir", "fchdir",
	"readlink", "readlinkat", "symlink", "link", "realpath", "dirname", "basename",
	"opendir", "readdir", "readdir_r", "closedir", "rewinddir", "dirfd",
	"fopen", "fdopen", "freopen", "fclose", "fread", "fwrite", "fgets", "fputs",
	"fprintf", "vfprintf", "sprintf", "snprintf", "vsnprintf", "vsprintf", "printf", "vprintf",
	"fflush", "fseek", "ftell", "fseeko", "ftello", "rewind", "fileno", "setvbuf",
	"setbuf", "ungetc", "ungetwc", "getc", "getwc", "putc", "fgetc", "fputc", "fputwc", "feof", "ferror", "clearerr",
	"tmpfile", "remove", "rename", "fopen64", "freopen64",
	"stdin", "stdout", "stderr", "fscanf", "sscanf", "vsscanf",
	// sockets
	"socket", "bind", "connect", "listen", "accept", "accept4", "shutdown",
	"send", "recv", "sendto", "recvfrom", "sendmsg", "recvmsg", "sendmmsg", "recvmmsg",
	"getsockopt", "setsockopt", "getsockname", "getpeername",
	"getaddrinfo", "freeaddrinfo", "gai_strerror", "getnameinfo",
	"inet_ntop", "inet_pton", "inet_aton", "htons", "ntohs", "htonl", "ntohl",
	"if_indextoname", "if_nametoindex", "in6addr_any", "in6addr_loopback",
	"socketpair", "poll", "ppoll", "select", "pselect",
	"epoll_create", "epoll_create1", "epoll_ctl", "epoll_wait", "epoll_pwait",
	"eventfd", "eventfd_read", "eventfd_write",
	"timerfd_create", "timerfd_settime", "timerfd_gettime",
	"inotify_init", "inotify_init1", "inotify_add_watch", "inotify_rm_watch",
	// time / signals
	"clock_gettime", "clock_getres", "clock_nanosleep", "nanosleep", "usleep",
	"gettimeofday", "settimeofday", "time", "clock", "difftime", "localtime", "localtime_r", "gmtime", "gmtime_r", "mktime",
	"strftime", "strftime_l", "asctime_r", "ctime_r", "tzset", "timezone", "daylight", "tzname",
	"sigaction", "sigaction64", "rt_sigaction", "__rt_sigaction",
	"sigprocmask", "sigpending", "sigsuspend", "sigaltstack",
	"sigemptyset", "sigfillset", "sigaddset", "sigdelset", "sigismember",
	"raise", "kill", "abort", "exit", "_exit", "_Exit", "quick_exit", "signal",
	"setjmp", "longjmp", "siglongjmp",
	// misc
	"syscall", "sysinfo", "uname", "getcwd", "getenv", "setenv", "unsetenv", "putenv",
	"secure_getenv", "clearenv", "qsort", "bsearch", "abs", "labs", "llabs", "ldiv",
	"rand", "srand", "random", "srandom",
	"sched_yield", "sched_getaffinity", "sched_setaffinity", "sched_getcpu",
	"sched_getparam", "sched_getscheduler", "sched_setscheduler",
	"sched_get_priority_min", "sched_get_priority_max", "nice", "getpriority", "setpriority",
	"getrlimit", "setrlimit", "getrusage", "prlimit",
	"uname", "sysconf", "pathconf", "fpathconf",
	"isatty", "ttyname_r", "tcgetattr", "tcsetattr",
	"__cxa_atexit", "__cxa_finalize",
	"dl_iterate_phdr",
	"open64", "lseek64", "mmap64",
	"posix_fadvise", "posix_fallocate", "flock", "lockf",
	"getdents64", "statfs", "fstatfs",
	"waitpid", "wait", "fork", "vfork", "execve", "execv", "execl",
	"clone", "unshare", "setns",
	"capget", "capset",
	"inotify_init1", "sem_init", "sem_destroy", "sem_wait", "sem_post",
	"statvfs", "utime", "utimes", "puts", "vasprintf",
	"__assert", "__cmsg_nxthdr", "__ctype_get_mb_cur_max",
	"btowc", "mbrlen", "mbrtowc", "mbsnrtowcs", "mbsrtowcs", "mbtowc", "wcrtomb", "wctob",
	"wcslen", "wcsnrtombs", "wmemchr", "wmemcmp",
	"newlocale", "freelocale", "uselocale", "localeconv",
	"isspace", "tolower", "iswalpha_l", "iswblank_l", "iswcntrl_l", "iswdigit_l",
	"iswlower_l", "iswprint_l", "iswpunct_l", "iswspace_l", "iswupper_l", "iswxdigit_l",
	"towlower_l", "towupper_l", "strcoll_l", "strxfrm_l", "wcscoll_l", "wcsxfrm_l",
	"strtold_l", "strtoll_l", "strtoull_l", "openlog", "closelog", "syslog", "optarg", "optind", "getopt_long", "gethostname", "gethostbyname", "ldexp",
}

// libmSymbols is a representative libm import set; Lookup uses host dlsym for any name.
var libmSymbols = []string{
	"sin", "cos", "tan", "asin", "acos", "atan", "atan2",
	"sinh", "cosh", "tanh", "asinh", "acosh", "atanh",
	"exp", "exp2", "expm1", "log", "log2", "log10", "log1p",
	"pow", "sqrt", "cbrt", "hypot", "ceil", "floor", "trunc", "round", "nearbyint", "rint",
	"fmod", "remainder", "remquo", "copysign", "fmin", "fmax", "fdim", "fabs",
	"ldexp", "frexp", "modf", "scalbn", "ilogb", "logb", "nextafter",
	"sincos", "sincosf", "sinf", "cosf", "tanf", "asinf", "acosf", "atanf", "powf", "powl", "sqrtf", "cbrtf", "expf", "exp2f", "logf",
	"floorf", "ceilf", "roundf", "fmodf", "atan2f", "fabsf", "ldexpf", "frexpf", "modff",
	"sinhf", "coshf", "tanhf", "hypotf", "log2f", "log10f", "nextafterf",
	"llround", "llroundf", "lround", "lroundf", "remainderf", "remquof", "erff", "erfcf", "fmal", "finitef",
	"nan", "nanf",
}

func init() {
	_ = libmSymbols
}
