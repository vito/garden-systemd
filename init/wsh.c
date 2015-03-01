#define _GNU_SOURCE

#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <termios.h>
#include <unistd.h>

#include "msg.h"
#include "pump.h"
#include "un.h"

typedef struct wsh_s wsh_t;

struct wsh_s {
  /* Path and args to execute */
  int argc;
  char **argv;

  /* Environment variables for running process */
  char **environment_variables;
  size_t environment_variable_count;

  /* Path to socket */
  const char *socket_path;

  /* User to change to */
  const char *user;

  /* Working directory of process */
  const char *dir;

  /* File to save container-namespaced pid of spawned process in to */
  const char *pid_file;
};

int wsh__usage(wsh_t *w) {
  fprintf(stderr, "Usage: %s OPTION...\n", w->argv[0]);
  fprintf(stderr, "\n");

  fprintf(stderr, "  --socket PATH   "
    "Path to socket"
    "\n");

  fprintf(stderr, "  --user USER     "
    "User to change to"
    "\n");

  fprintf(stderr, "  --env KEY=VALUE "
    "Environment variables to set for the command. "
    "You can specify multiple --env arguments"
    "\n");

  fprintf(stderr, "  --dir PATH      "
    "Working directory for the running process"
    "\n");

  fprintf(stderr, "  --pidfile PIDFILE      "
    "File to save container-namespaced pid of spawned process to"
    "\n");

  fprintf(stderr, "  --rsh           "
    "RSH compatibility mode"
    "\n");
  return 0;
}

int wsh__getopt(wsh_t *w) {
  int i = 1;
  int j = w->argc - i;

  w->pid_file = 0;

  while (i < w->argc) {
    if (w->argv[i][0] != '-') {
      break;
    }

    if (j >= 1 && ((strcmp(w->argv[i], "-h") == 0) || (strcmp(w->argv[i], "--help") == 0))) {
      wsh__usage(w);
      return -1;
    } else if (j >= 2 && strcmp(w->argv[i], "--socket") == 0) {
      w->socket_path = strdup(w->argv[i+1]);
      i += 2;
      j -= 2;
    } else if (j >= 2 && strcmp(w->argv[i], "--user") == 0) {
      w->user = strdup(w->argv[i+1]);
      i += 2;
      j -= 2;
    } else if (j >= 2 && strcmp(w->argv[i], "--dir") == 0) {
      w->dir = strdup(w->argv[i+1]);
      i += 2;
      j -= 2;
    } else if (j >= 2 && strcmp(w->argv[i], "--pidfile") == 0) {
      w->pid_file = strdup(w->argv[i+1]);
      i += 2;
      j -= 2;
    } else if (j >= 2 && strcmp(w->argv[i], "--env") == 0) {
      w->environment_variable_count++;
      w->environment_variables = realloc(w->environment_variables, w->environment_variable_count * sizeof(char *));
      w->environment_variables[w->environment_variable_count - 1] = strdup(w->argv[i+1]);
      i += 2;
      j -= 2;
    } else if (j >= 1 && strcmp(w->argv[i], "--rsh") == 0) {
      i += 1;
      j -= 1;

      /* rsh [-46dn] [-l username] [-t timeout] host [command] */
      while (i < w->argc) {
        if (w->argv[i][0] != '-') {
          break;
        }

        if (j >= 1 && strlen(w->argv[i]) == 2 && strchr("46dn", w->argv[i][1])) {
          /* Ignore */
          i += 1;
          j -= 1;
        } else if (j >= 2 && strlen(w->argv[i]) == 2 && w->argv[i][1] == 'l') {
          w->user = strdup(w->argv[i+1]);
          i += 2;
          j -= 2;
        } else if (j >= 2 && strlen(w->argv[i]) == 2 && w->argv[i][1] == 't') {
          /* Ignore */
          i += 2;
          j -= 2;
        } else {
          goto invalid;
        }
      }

      /* Skip over host */
      assert(i < w->argc);
      i += 1;
      j -= 1;
    } else {
      goto invalid;
    }
  }

  w->argc = w->argc - i;
  if (w->argc) {
    w->argv = &w->argv[i];
  } else {
    w->argv = NULL;
  }

  return 0;

invalid:
  fprintf(stderr, "%s: invalid option -- %s\n", w->argv[0], w->argv[i]);
  fprintf(stderr, "Try `%s --help' for more information.\n", w->argv[0]);
  return -1;
}

static pid_t pid;

void cleanup_pidfile(const char *pidfile, int pid_fd) {
  int rv;

  if (pidfile) {
    rv = unlink(pidfile);
    if (rv != 0) {
      perror("unlink pidfile");
      exit(255);
    }
  }
}

void pump_loop(const char *pid_file, pump_t *p, int pid_fd, int exit_status_fd, pump_pair_t *pp, int pplen) {
  int i, rv, pidfd;
  char pidstr[10];

  rv = read(pid_fd, &pid, sizeof(pid));
  assert(rv >= 0);

  if (pid_file) {
    pidfd = open(pid_file, O_RDWR|O_CREAT, 0600);
    if (pidfd == -1 ) {
      perror("open pidfile");
      exit(1);
    }

    sprintf(pidstr, "%d\n", pid);
    write(pidfd, pidstr, strlen(pidstr));

    rv = close(pid_fd);
    if (rv != 0) {
      perror("close pidfile");
    }
  }

  for (;;) {
    pump_init(p);

    for (i = 0; i < pplen; i++) {
      pump_add_pair(p, &pp[i]);
    }

    if (exit_status_fd >= 0) {
      pump_add_fd(p, exit_status_fd, PUMP_READ | PUMP_EXCEPT);
    }

    do {
      rv = pump_select(p);
    } while (rv == -1 && errno == EINTR);

    if (rv == -1) {
      perror("select");
      cleanup_pidfile(pid_file, pidfd);
      abort();
    }

    for (i = 0; i < pplen; i++) {
      pump_pair_copy(&pp[i]);
    }

    if (pump_ready(p, exit_status_fd, PUMP_READ | PUMP_EXCEPT)) {
      int status;

      rv = read(exit_status_fd, &status, sizeof(status));
      assert(rv >= 0);

      /* One more splice to make sure kernel buffers are emptied */
      for (i = 0; i < pplen; i++) {
        pump_pair_copy(&pp[i]);
      }

      if (rv == 0) {
        /* EOF: process terminated by signal */
        cleanup_pidfile(pid_file, pidfd);
        exit(255);
      }

      assert(rv == sizeof(status));
      cleanup_pidfile(pid_file, pidfd);
      exit(status);
    }
  }
}

static int pty_local_fd, pty_remote_fd;
static struct termios told, tnew;
static struct winsize wsz;

void tty_reset(void) {
  int rv;

  rv = tcsetattr(pty_local_fd, TCSANOW, &told);
  assert(rv != -1);
}

void tty__atexit(void) {
  tty_reset();
}

void tty_raw(void) {
  int rv;

  rv = tcgetattr(pty_local_fd, &told);
  assert(rv != -1);

  rv = atexit(tty__atexit);
  assert(rv != -1);

  tnew = told;
  cfmakeraw(&tnew);

  rv = tcsetattr(pty_local_fd, TCSANOW, &tnew);
  assert(rv != -1);
}

void tty_gwinsz(void) {
  int rv;

  rv = ioctl(pty_local_fd, TIOCGWINSZ, &wsz);
  assert(rv != -1);
}

void tty_swinsz(void) {
  int rv;

  rv = ioctl(pty_remote_fd, TIOCSWINSZ, &wsz);
  assert(rv != -1);
}

void tty__sigwinch(int sig) {
  tty_gwinsz();
  tty_swinsz();
}

void tty_winsz(void) {
  sighandler_t s;

  /* Setup handler for window size */
  s = signal(SIGWINCH, tty__sigwinch);
  assert(s != SIG_ERR);

  /* Figure out window size and forward it to the remote pty */
  tty_gwinsz();
  tty_swinsz();
}

void loop_interactive(const char *pidfile, int fd) {
  msg_response_t res;
  int fds[3];
  size_t fdslen = sizeof(fds)/sizeof(fds[0]);
  int rv;

  rv = un_recv_fds(fd, (char *)&res, sizeof(res), fds, fdslen);
  if (rv <= 0) {
    perror("recvmsg");
    exit(255);
  }

  assert(rv == sizeof(res));

  pty_remote_fd = fds[0];
  pty_local_fd = STDIN_FILENO;

  tty_raw();
  tty_winsz();

  pump_t p;
  pump_pair_t pp[2];

  /* Use duplicates to decouple input/output */
  pump_pair_init(&pp[0], &p, STDIN_FILENO, dup(fds[0]));
  pump_pair_init(&pp[1], &p, dup(fds[0]), STDOUT_FILENO);

  pump_loop(pidfile, &p, fds[2], fds[1], pp, 2);
}

void loop_noninteractive(const char* pidfile, int fd) {
  msg_response_t res;
  int fds[5];
  size_t fdslen = sizeof(fds)/sizeof(fds[0]);
  int rv;

  rv = un_recv_fds(fd, (char *)&res, sizeof(res), fds, fdslen);
  if (rv <= 0) {
    perror("recvmsg");
    exit(255);
  }

  assert(rv == sizeof(res));

  pump_t p;
  pump_pair_t pp[3];

  pump_pair_init(&pp[0], &p, STDIN_FILENO, fds[0]);
  pump_pair_init(&pp[1], &p, fds[1], STDOUT_FILENO);
  pump_pair_init(&pp[2], &p, fds[2], STDERR_FILENO);

  pump_loop(pidfile, &p, fds[4], fds[3], pp, 3);
}

int main(int argc, char **argv) {
  wsh_t *w;
  int rv;
  int fd;
  msg_request_t req;

  signal(SIGPIPE, SIG_IGN);

  w = calloc(1, sizeof(*w));
  assert(w != NULL);

  w->argc = argc;
  w->argv = argv;

  rv = wsh__getopt(w);
  if (rv == -1) {
    exit(1);
  }

  if (w->socket_path == NULL) {
    w->socket_path = "run/wshd.sock";
  }

  rv = un_connect(w->socket_path);
  if (rv < 0) {
    perror("connect");
    exit(255);
  }

  fd = rv;

  msg_request_init(&req);

  msg_dir_import(&req.dir, w->dir);

  if (isatty(STDIN_FILENO)) {
    req.tty = 1;
  } else {
    req.tty = 0;
  }

  rv = msg_array_import(&req.arg, w->argc, (const char **)w->argv);
  if (rv == -1) {
    fprintf(stderr, "msg_import_array: Too much data in args\n");
    exit(255);
  }

  rv = msg_array_import(&req.env, w->environment_variable_count, (const char **)w->environment_variables);
  if (rv == -1) {
    fprintf(stderr, "msg_import_array: Too much data in environment variables\n");
    exit(255);
  }

  rv = msg_rlimit_import(&req.rlim);
  if (rv == -1) {
    fprintf(stderr, "msg_rlimit_import: %s\n", strerror(errno));
    exit(255);
  }

  rv = msg_user_import(&req.user, w->user);
  if (rv == -1) {
    fprintf(stderr, "msg_user_import: %s\n", strerror(errno));
    exit(255);
  }

  rv = un_send_fds(fd, (char *)&req, sizeof(req), NULL, 0);
  if (rv <= 0) {
    perror("sendmsg");
    exit(255);
  }

  if (req.tty) {
    loop_interactive(w->pid_file, fd);
  } else {
    loop_noninteractive(w->pid_file, fd);
  }

  perror("unreachable");
  exit(255);
}
