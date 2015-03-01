#define _GNU_SOURCE

#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/ipc.h>
#include <sys/mount.h>
#include <sys/param.h>
#include <sys/resource.h>
#include <sys/shm.h>
#include <sys/signalfd.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <termios.h>
#include <unistd.h>

#include "msg.h"
#include "pty.h"
#include "pwd.h"
#include "un.h"
#include "util.h"

typedef struct wshd_s wshd_t;

struct wshd_s {
  /* Path to directory where server socket is placed */
  char run_path[256];

  /* File descriptor of listening socket */
  int fd;

  /* Map pids to exit status fds */
  struct {
    pid_t pid;
    int fd;
  } *pid_to_fd;
  size_t pid_to_fd_len;
};

int wshd__usage(wshd_t *w, int argc, char **argv) {
  fprintf(stderr, "Usage: %s OPTION...\n", argv[0]);
  fprintf(stderr, "\n");

  fprintf(stderr, "  --run PATH   "
    "Directory where server socket is placed"
    "\n");

  return 0;
}

int wshd__getopt(wshd_t *w, int argc, char **argv) {
  int i = 1;
  int j = argc - i;
  int rv;

  while (i < argc) {
    if (j >= 2) {
      if (strcmp("--run", argv[i]) == 0) {
        rv = snprintf(w->run_path, sizeof(w->run_path), "%s", argv[i+1]);
        if (rv >= sizeof(w->run_path)) {
          goto toolong;
        }
      } else {
        goto invalid;
      }

      i += 2;
      j -= 2;
    } else if (j == 1) {
      if (strcmp("-h", argv[i]) == 0 ||
          strcmp("--help", argv[i]) == 0)
      {
        wshd__usage(w, argc, argv);
        return -1;
      } else {
        goto invalid;
      }
    } else {
      assert(NULL);
    }
  }

  return 0;

toolong:
  fprintf(stderr, "%s: argument too long -- %s\n", argv[0], argv[i]);
  fprintf(stderr, "Try `%s --help' for more information.\n", argv[0]);
  return -1;

invalid:
  fprintf(stderr, "%s: invalid option -- %s\n", argv[0], argv[i]);
  fprintf(stderr, "Try `%s --help' for more information.\n", argv[0]);
  return -1;
}

void assert_directory(const char *path) {
  int rv;
  struct stat st;

  rv = stat(path, &st);
  if (rv == -1) {
    fprintf(stderr, "stat(\"%s\"): %s\n", path, strerror(errno));
    exit(1);
  }

  if (!S_ISDIR(st.st_mode)) {
    fprintf(stderr, "stat(\"%s\"): %s\n", path, "No such directory");
    exit(1);
  }
}

void child_pid_to_fd_add(wshd_t *w, pid_t pid, int fd) {
  int len = w->pid_to_fd_len;

  /* Store a copy */
  fd = dup(fd);
  if (fd == -1) {
    perror("dup");
    abort();
  }

  w->pid_to_fd = realloc(w->pid_to_fd, (len + 1) * sizeof(w->pid_to_fd[0]));
  assert(w->pid_to_fd != NULL);

  w->pid_to_fd[len].pid = pid;
  w->pid_to_fd[len].fd = fd;
  w->pid_to_fd_len++;
}

int child_pid_to_fd_remove(wshd_t *w, pid_t pid) {
  int i;
  int len = w->pid_to_fd_len;
  int fd = -1;

  for (i = 0; i < len; i++) {
    if (w->pid_to_fd[i].pid == pid) {
      fd = w->pid_to_fd[i].fd;

      /* Move tail if there is one */
      if ((i + 1) < len) {
        memmove(&w->pid_to_fd[i], &w->pid_to_fd[i+1], (len - i - 1) * sizeof(w->pid_to_fd[0]));
      }

      w->pid_to_fd = realloc(w->pid_to_fd, (w->pid_to_fd_len - 1) * sizeof(w->pid_to_fd[0]));
      w->pid_to_fd_len--;

      if (w->pid_to_fd_len) {
        assert(w->pid_to_fd != NULL);
      } else {
        assert(w->pid_to_fd == NULL);
      }

      break;
    }
  }

  return fd;
}

char **env__add(char **envp, const char *key, const char *value) {
  size_t envplen = 0;
  char *buf;
  size_t buflen;
  int rv;

  if (envp == NULL) {
    /* Trailing NULL */
    envplen = 1;
  } else {
    while(envp[envplen++] != NULL);
  }

  envp = realloc(envp, sizeof(envp[0]) * (envplen + 1));
  assert(envp != NULL);

  buflen = strlen(key) + 1 + strlen(value) + 1;
  buf = malloc(buflen);
  assert(buf != NULL);

  rv = snprintf(buf, buflen, "%s=%s", key, value);
  assert(rv == buflen - 1);

  envp[envplen - 1] = buf;
  envp[envplen] = NULL;

  return envp;
}

const char* env__get(char **envp, const char* key) {
  if (envp != NULL) {
    int i = 0;
    while (envp[i] != NULL) {
      char* eq = strchr(envp[i], '=');
      if (eq != NULL) {
        size_t keyLen = eq - envp[i];
        if (strlen(key) == keyLen) {
          if (memcmp(key, envp[i], keyLen) == 0) {
            return eq + 1;
          }
        }
      }
      i++;
    }
  }

  return NULL;
}

char **child_setup_environment(struct passwd *pw, char **extra_env_vars) {
  int rv;
  char **envp = extra_env_vars;

  rv = chdir(pw->pw_dir);
  if (rv == -1) {
    perror("chdir");
    return NULL;
  }

  envp = env__add(envp, "HOME", pw->pw_dir);
  envp = env__add(envp, "USER", pw->pw_name);

  // Use $PATH if provided, otherwise default depending on uid.
  const char * envp_path = env__get(envp, "PATH");
  if (envp_path != NULL) {
      setenv("PATH", envp_path, 1);
  } else if (pw->pw_uid == 0) {
    const char *sanitizedRootPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
    envp = env__add(envp, "PATH", sanitizedRootPath);
    setenv("PATH", sanitizedRootPath, 1);
  } else {
    const char *sanitizedUserPath = "/usr/local/bin:/usr/bin:/bin";
    envp = env__add(envp, "PATH", sanitizedUserPath);
    setenv("PATH", sanitizedUserPath, 1);
  }

  return envp;
}

int child_fork(msg_request_t *req, int in, int out, int err) {
  int rv;

  rv = fork();
  if (rv == -1) {
    perror("fork");
    exit(1);
  }

  if (rv == 0) {
    const char *user;
    struct passwd *pw;
    char *default_argv[] = { "/bin/sh", NULL };
    char *default_envp[] = { NULL };
    char **argv = default_argv;
    char **envp = default_envp;
    char **extra_env_vars = NULL;

    rv = dup2(in, STDIN_FILENO);
    assert(rv != -1);

    rv = dup2(out, STDOUT_FILENO);
    assert(rv != -1);

    rv = dup2(err, STDERR_FILENO);
    assert(rv != -1);

    rv = setsid();
    assert(rv != -1);

    user = req->user.name;
    if (!strlen(user)) {
      user = "root";
    }

    pw = getpwnam(user);
    if (pw == NULL) {
      perror("getpwnam");
      goto error;
    }

    if (strlen(pw->pw_shell)) {
      default_argv[0] = strdup(pw->pw_shell);
    }

    /* Set controlling terminal if needed */
    if (isatty(in)) {
      rv = ioctl(STDIN_FILENO, TIOCSCTTY, 1);
      assert(rv != -1);
    }

    /* Use argv from request if needed */
    if (req->arg.count) {
      argv = (char **)msg_array_export(&req->arg);
      assert(argv != NULL);
    }

    rv = msg_rlimit_export(&req->rlim);
    if (rv == -1) {
      perror("msg_rlimit_export");
      goto error;
    }

    rv = msg_user_export(&req->user, pw);
    if (rv == -1) {
      perror("msg_user_export");
      goto error;
    }

    if (req->env.count) {
      extra_env_vars = (char **)msg_array_export(&req->env);
      assert(extra_env_vars != NULL);
    }

    envp = child_setup_environment(pw, extra_env_vars);
    assert(envp != NULL);

    if (strlen(req->dir.path)) {
      rv = chdir(req->dir.path);
      if (rv == -1) {
        perror("chdir");
        goto error;
      }
    }

    // don't mask signals of child process
    sigset_t mask;
    sigemptyset(&mask);
    sigprocmask(SIG_SETMASK, &mask, NULL);

    execvpe(argv[0], argv, envp);
    perror("execvpe");

error:
    exit(255);
  }

  return rv;
}

int child_handle_interactive(int fd, wshd_t *w, msg_request_t *req) {
  int i, j;
  int num_descriptors = 3;
  int p[num_descriptors][2];
  int p_[num_descriptors];
  int rv;
  msg_response_t res;

  msg_response_init(&res);

  /* Initialize so that the error handler can do its job */
  for (i = 0; i < num_descriptors; i++) {
    p[i][0] = -1;
    p[i][1] = -1;
    p_[i] = -1;
  }

  for (i = 1; i < num_descriptors; i++) {
    rv = pipe(p[i]);
    if (rv == -1) {
      perror("pipe");
      abort();
    }

    fcntl_mix_cloexec(p[i][0]);
    fcntl_mix_cloexec(p[i][1]);
  }

  rv = openpty(&p[0][0], &p[0][1], NULL);
  if (rv < 0) {
    perror("openpty");
    abort();
  }

  fcntl_mix_cloexec(p[0][0]);
  fcntl_mix_cloexec(p[0][1]);

  /* Descriptors to send to client */
  p_[0] = p[0][0];
  p_[1] = p[1][0];
  p_[2] = p[2][0];

  rv = un_send_fds(fd, (char *)&res, sizeof(res), p_, num_descriptors);
  if (rv == -1) {
    goto err;
  }

  rv = child_fork(req, p[0][1], p[0][1], p[0][1]);
  assert(rv > 0);

  write(p[2][1], &rv, sizeof(rv));

  child_pid_to_fd_add(w, rv, p[1][1]);

err:
  for (i = 0; i < 3; i++) {
    for (j = 0; j < 2; j++) {
      if (p[i][j] > -1) {
        close(p[i][j]);
        p[i][j] = -1;
      }
    }
  }

  if (fd > -1) {
    close(fd);
    fd = -1;
  }

  return 0;
}

int child_handle_noninteractive(int fd, wshd_t *w, msg_request_t *req) {
  int i, j;
  int num_descriptors = 5;
  int p[num_descriptors][2];
  int p_[num_descriptors];
  int rv;
  msg_response_t res;

  msg_response_init(&res);

  /* Initialize so that the error handler can do its job */
  for (i = 0; i < num_descriptors; i++) {
    p[i][0] = -1;
    p[i][1] = -1;
    p_[i] = -1;
  }

  for (i = 0; i < num_descriptors; i++) {
    rv = pipe(p[i]);
    if (rv == -1) {
      perror("pipe");
      abort();
    }

    fcntl_mix_cloexec(p[i][0]);
    fcntl_mix_cloexec(p[i][1]);
  }

  /* Descriptors to send to client */
  p_[0] = p[0][1];
  p_[1] = p[1][0];
  p_[2] = p[2][0];
  p_[3] = p[3][0];
  p_[4] = p[4][0];

  rv = un_send_fds(fd, (char *)&res, sizeof(res), p_, num_descriptors);
  if (rv == -1) {
    goto err;
  }

  rv = child_fork(req, p[0][0], p[1][1], p[2][1]);
  assert(rv > 0);

  write(p[4][1], &rv, sizeof(rv));

  child_pid_to_fd_add(w, rv, p[3][1]);

err:
  for (i = 0; i < 5; i++) {
    for (j = 0; j < 2; j++) {
      if (p[i][j] > -1) {
        close(p[i][j]);
        p[i][j] = -1;
      }
    }
  }

  if (fd > -1) {
    close(fd);
    fd = -1;
  }

  return 0;
}

int child_accept(wshd_t *w) {
  int rv, fd;
  msg_request_t req;

  rv = accept(w->fd, NULL, NULL);
  if (rv == -1) {
    perror("accept");
    abort();
  }

  fd = rv;

  fcntl_mix_cloexec(fd);

  rv = un_recv_fds(fd, (char *)&req, sizeof(req), NULL, 0);
  if (rv < 0) {
    perror("recvmsg");
    exit(255);
  }

  if (rv == 0) {
    close(fd);
    return 0;
  }

  assert(rv == sizeof(req));

  if (req.tty) {
    return child_handle_interactive(fd, w, &req);
  } else {
    return child_handle_noninteractive(fd, w, &req);
  }
}

void child_handle_sigchld(wshd_t *w) {
  pid_t pid;
  int status, exitstatus;
  int fd;

  while (1) {
    do {
      pid = waitpid(-1, &status, WNOHANG);
    } while (pid == -1 && errno == EINTR);

    /* Break when there are no more children */
    if (pid <= 0) {
      break;
    }

    /* Processes can be reparented, so a pid may not map to an fd */
    fd = child_pid_to_fd_remove(w, pid);
    if (fd == -1) {
      continue;
    }

    if (WIFEXITED(status)) {
      exitstatus = WEXITSTATUS(status);

      /* Send exit status to client */
      write(fd, &exitstatus, sizeof(exitstatus));
    } else {
      assert(WIFSIGNALED(status));

      /* No exit status */
    }

    close(fd);
  }
}

int child_signalfd(void) {
  sigset_t mask;
  int rv;
  int fd;

  sigemptyset(&mask);
  sigaddset(&mask, SIGCHLD);

  rv = sigprocmask(SIG_BLOCK, &mask, NULL);
  if (rv == -1) {
    perror("sigprocmask");
    abort();
  }

  fd = signalfd(-1, &mask, SFD_NONBLOCK | SFD_CLOEXEC);
  if (fd == -1) {
    perror("signalfd");
    abort();
  }

  return fd;
}

int child_loop(wshd_t *w) {
  int sfd;
  int rv;

  close(STDIN_FILENO);
  close(STDOUT_FILENO);
  close(STDERR_FILENO);

  sfd = child_signalfd();

  for (;;) {
    fd_set fds;

    FD_ZERO(&fds);
    FD_SET(w->fd, &fds);
    FD_SET(sfd, &fds);

    do {
      rv = select(FD_SETSIZE, &fds, NULL, NULL, NULL);
    } while (rv == -1 && errno == EINTR);

    if (rv == -1) {
      perror("select");
      abort();
    }

    if (FD_ISSET(w->fd, &fds)) {
      child_accept(w);
    }

    if (FD_ISSET(sfd, &fds)) {
      struct signalfd_siginfo fdsi;

      rv = read(sfd, &fdsi, sizeof(fdsi));
      assert(rv == sizeof(fdsi));

      /* Ignore siginfo and loop waitpid to catch all children */
      child_handle_sigchld(w);
    }
  }

  return 1;
}

int child_run(void *data) {
  wshd_t *w = (wshd_t *)data;
  int rv;

  /* Detach this process from its original group */
  rv = setsid();
  if (rv == -1) {
    perror("setsid");
    return rv;
  }

  assert(rv > 0 && rv == getpid());

  return child_loop(w);
}

/* Returns the maximum allowed number of open files. */
long int max_nr_open() {
  char file_data[32];
  size_t bytes_read;
  FILE *f;
  long int nr;

  if ((f = fopen("/proc/sys/fs/nr_open", "r")) == NULL) {
    perror("Failed to open /proc/sys/fs/nr_open");
    abort();
  }

  bytes_read = fread(file_data, 1, sizeof(file_data), f);
  if (ferror(f) || bytes_read == 0) {
    perror("Failed to read /proc/sys/fs/nr_open");
    abort();
  }

  if (fclose(f)) {
    perror("Failed to close /proc/sys/fs/nr_open");
    abort();
  }

  errno = 0;
  nr = strtol(file_data, NULL, 10);
  if (errno) {
    perror("Contents of /proc/sys/fs/nr_open could not be converted to a long int");
    abort();
  }
  return nr;
}

/* Sets a hard resource limit to specified value. */
void set_hard_rlimit(char * resource_name, int resource, rlim_t hard_limit) {
  char err_text[1024];
  struct rlimit lim = {0, 0};
  if (getrlimit(resource, &lim)) {
    strcpy(err_text, "getrlimit failed to return ");
    strcat(err_text, resource_name);
    perror(err_text);
    abort();
  }

  lim.rlim_max = hard_limit;
  if (setrlimit(resource, &lim)) {
    strcpy(err_text, "setrlimit failed to set ");
    strcat(err_text, resource_name);
    perror(err_text);
    abort();
  }
}

/* Sets hard resource limits to their maximum permitted values. */
void set_hard_rlimits() {
  set_hard_rlimit("RLIMIT_AS", RLIMIT_AS, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_CORE", RLIMIT_CORE, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_CPU", RLIMIT_CPU, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_DATA", RLIMIT_DATA, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_FSIZE", RLIMIT_FSIZE, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_LOCKS", RLIMIT_LOCKS, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_MEMLOCK", RLIMIT_MEMLOCK, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_MSGQUEUE", RLIMIT_MSGQUEUE, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_NICE", RLIMIT_NICE, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_NOFILE", RLIMIT_NOFILE, max_nr_open());
  set_hard_rlimit("RLIMIT_NPROC", RLIMIT_NPROC, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_RSS", RLIMIT_RSS, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_RTPRIO", RLIMIT_RTPRIO, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_SIGPENDING", RLIMIT_SIGPENDING, RLIM_INFINITY);
  set_hard_rlimit("RLIMIT_STACK", RLIMIT_STACK, RLIM_INFINITY);
}

pid_t child_start(wshd_t *w) {
  long pagesize;
  void *stack;
  int flags = 0;
  pid_t pid;

  pagesize = sysconf(_SC_PAGESIZE);
  stack = alloca(pagesize);
  assert(stack != NULL);

  /* Point to top of stack (it grows down) */
  stack = stack + pagesize;

  pid = clone(child_run, stack, flags, w);
  if (pid == -1) {
    perror("clone");
    abort();
  }

  return pid;
}

int parent_run(wshd_t *w) {
  char path[MAXPATHLEN];
  int rv;
  /* pid_t pid; */

  memset(path, 0, sizeof(path));

  strcpy(path + strlen(path), w->run_path);
  strcpy(path + strlen(path), "/");
  strcpy(path + strlen(path), "wshd.sock");

  w->fd = un_listen(path);

  /* Unmount directory containing socket file to prevent
   * spawned processes from accessing it. */
  rv = umount2(w->run_path, MNT_DETACH);
  if (rv != 0) {
    perror("umount run path");
    exit(1);
  }

  /* Set hard resource limits to their maximum values so that soft and
     hard resource limits can be set to arbitrary values even in an
     unprivileged container. */
  set_hard_rlimits();

  return child_loop(w);
}

int main(int argc, char **argv) {
  wshd_t *w;
  int rv;

  w = calloc(1, sizeof(*w));
  assert(w != NULL);

  rv = wshd__getopt(w, argc, argv);
  if (rv == -1) {
    exit(1);
  }

  if (strlen(w->run_path) == 0) {
    strcpy(w->run_path, "run");
  }

  mkdir(w->run_path, 0755);
  assert_directory(w->run_path);

  return parent_run(w);
}
