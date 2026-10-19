package store

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;
typedef void (*sqlite3_destructor_type)(void*);

static void* lib_handle = NULL;
static int (*fn_open)(const char*, sqlite3**) = NULL;
static int (*fn_close)(sqlite3*) = NULL;
static int (*fn_exec)(sqlite3*, const char*, int (*)(void*,int,char**,char**), void*, char**) = NULL;
static void (*fn_free)(void*) = NULL;
static const char* (*fn_errmsg)(sqlite3*) = NULL;
static int (*fn_prepare_v2)(sqlite3*, const char*, int, sqlite3_stmt**, const char**) = NULL;
static int (*fn_step)(sqlite3_stmt*) = NULL;
static int (*fn_finalize)(sqlite3_stmt*) = NULL;
static int (*fn_bind_text)(sqlite3_stmt*, int, const char*, int, sqlite3_destructor_type) = NULL;
static int (*fn_bind_int64)(sqlite3_stmt*, int, long long) = NULL;
static int (*fn_bind_double)(sqlite3_stmt*, int, double) = NULL;
static const unsigned char* (*fn_column_text)(sqlite3_stmt*, int) = NULL;
static long long (*fn_column_int64)(sqlite3_stmt*, int) = NULL;
static double (*fn_column_double)(sqlite3_stmt*, int) = NULL;
static int (*fn_busy_timeout)(sqlite3*, int) = NULL;

static int init_sqlite_lib() {
    if (lib_handle != NULL) return 0;
    lib_handle = dlopen("libsqlite3.so.0", RTLD_LAZY | RTLD_GLOBAL);
    if (!lib_handle) {
        lib_handle = dlopen("/usr/lib64/libsqlite3.so.0", RTLD_LAZY | RTLD_GLOBAL);
    }
    if (!lib_handle) return -1;

    fn_open = dlsym(lib_handle, "sqlite3_open");
    fn_close = dlsym(lib_handle, "sqlite3_close");
    fn_exec = dlsym(lib_handle, "sqlite3_exec");
    fn_free = dlsym(lib_handle, "sqlite3_free");
    fn_errmsg = dlsym(lib_handle, "sqlite3_errmsg");
    fn_prepare_v2 = dlsym(lib_handle, "sqlite3_prepare_v2");
    fn_step = dlsym(lib_handle, "sqlite3_step");
    fn_finalize = dlsym(lib_handle, "sqlite3_finalize");
    fn_bind_text = dlsym(lib_handle, "sqlite3_bind_text");
    fn_bind_int64 = dlsym(lib_handle, "sqlite3_bind_int64");
    fn_bind_double = dlsym(lib_handle, "sqlite3_bind_double");
    fn_column_text = dlsym(lib_handle, "sqlite3_column_text");
    fn_column_int64 = dlsym(lib_handle, "sqlite3_column_int64");
    fn_column_double = dlsym(lib_handle, "sqlite3_column_double");
    fn_busy_timeout = dlsym(lib_handle, "sqlite3_busy_timeout");
    return 0;
}

static int c_sqlite_open(const char* path, sqlite3** db) {
    if (init_sqlite_lib() != 0) return -1;
    int rc = fn_open(path, db);
    if (rc == 0 && fn_busy_timeout != NULL) {
        fn_busy_timeout(*db, 5000);
    }
    return rc;
}

static int c_sqlite_close(sqlite3* db) { return fn_close(db); }

static int c_sqlite_exec(sqlite3* db, const char* sql, char** errmsg) {
    return fn_exec(db, sql, NULL, NULL, errmsg);
}

static void c_sqlite_free(void* p) { if (fn_free && p) fn_free(p); }
static const char* c_sqlite_errmsg(sqlite3* db) { return fn_errmsg(db); }

static int c_sqlite_prepare(sqlite3* db, const char* sql, sqlite3_stmt** stmt) {
    return fn_prepare_v2(db, sql, -1, stmt, NULL);
}

static int c_sqlite_step(sqlite3_stmt* stmt) { return fn_step(stmt); }
static int c_sqlite_finalize(sqlite3_stmt* stmt) { return fn_finalize(stmt); }

static int c_sqlite_bind_text(sqlite3_stmt* stmt, int idx, const char* val) {
    // SQLITE_TRANSIENT is ((sqlite3_destructor_type)-1)
    return fn_bind_text(stmt, idx, val, -1, (sqlite3_destructor_type)-1);
}
static int c_sqlite_bind_int64(sqlite3_stmt* stmt, int idx, long long val) {
    return fn_bind_int64(stmt, idx, val);
}
static int c_sqlite_bind_double(sqlite3_stmt* stmt, int idx, double val) {
    return fn_bind_double(stmt, idx, val);
}

static const char* c_sqlite_column_text(sqlite3_stmt* stmt, int col) {
    return (const char*)fn_column_text(stmt, col);
}
static long long c_sqlite_column_int64(sqlite3_stmt* stmt, int col) {
    return fn_column_int64(stmt, col);
}
static double c_sqlite_column_double(sqlite3_stmt* stmt, int col) {
    return fn_column_double(stmt, col);
}
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

const (
	sqliteOK   = 0
	sqliteRow  = 100
	sqliteDone = 101
)

type sqliteConn struct {
	mu sync.Mutex
	db *C.sqlite3
}

func openSQLite(path string) (*sqliteConn, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var db *C.sqlite3
	rc := C.c_sqlite_open(cPath, &db)
	if rc != sqliteOK {
		return nil, fmt.Errorf("sqlite3_open failed (rc=%d)", int(rc))
	}
	conn := &sqliteConn{db: db}
	if err := conn.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON;"); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (c *sqliteConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil {
		C.c_sqlite_close(c.db)
		c.db = nil
	}
	return nil
}

func (c *sqliteConn) Exec(sql string, args ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(args) == 0 {
		cSQL := C.CString(sql)
		defer C.free(unsafe.Pointer(cSQL))
		var errMsg *C.char
		rc := C.c_sqlite_exec(c.db, cSQL, &errMsg)
		if rc != sqliteOK {
			msg := ""
			if errMsg != nil {
				msg = C.GoString(errMsg)
				C.c_sqlite_free(unsafe.Pointer(errMsg))
			}
			return fmt.Errorf("sqlite exec error (rc=%d): %s", int(rc), msg)
		}
		return nil
	}

	stmt, err := c.prepareLocked(sql, args...)
	if err != nil {
		return err
	}
	defer C.c_sqlite_finalize(stmt)

	rc := C.c_sqlite_step(stmt)
	if rc != sqliteDone && rc != sqliteRow && rc != sqliteOK {
		return fmt.Errorf("sqlite step error (rc=%d): %s", int(rc), C.GoString(C.c_sqlite_errmsg(c.db)))
	}
	return nil
}

func (c *sqliteConn) prepareLocked(sql string, args ...any) (*C.sqlite3_stmt, error) {
	cSQL := C.CString(sql)
	defer C.free(unsafe.Pointer(cSQL))

	var stmt *C.sqlite3_stmt
	rc := C.c_sqlite_prepare(c.db, cSQL, &stmt)
	if rc != sqliteOK {
		return nil, fmt.Errorf("sqlite prepare error (rc=%d): %s", int(rc), C.GoString(C.c_sqlite_errmsg(c.db)))
	}

	for i, arg := range args {
		idx := C.int(i + 1)
		switch v := arg.(type) {
		case string:
			cs := C.CString(v)
			rc = C.c_sqlite_bind_text(stmt, idx, cs)
			C.free(unsafe.Pointer(cs))
		case int:
			rc = C.c_sqlite_bind_int64(stmt, idx, C.longlong(v))
		case int64:
			rc = C.c_sqlite_bind_int64(stmt, idx, C.longlong(v))
		case float64:
			rc = C.c_sqlite_bind_double(stmt, idx, C.double(v))
		case bool:
			b := 0
			if v {
				b = 1
			}
			rc = C.c_sqlite_bind_int64(stmt, idx, C.longlong(b))
		default:
			cs := C.CString(fmt.Sprintf("%v", v))
			rc = C.c_sqlite_bind_text(stmt, idx, cs)
			C.free(unsafe.Pointer(cs))
		}
		if rc != sqliteOK {
			C.c_sqlite_finalize(stmt)
			return nil, fmt.Errorf("sqlite bind param %d error (rc=%d)", i+1, int(rc))
		}
	}
	return stmt, nil
}

func C_step(stmt *C.sqlite3_stmt) int {
	return int(C.c_sqlite_step(stmt))
}

func C_finalize(stmt *C.sqlite3_stmt) {
	C.c_sqlite_finalize(stmt)
}

func C_colText(stmt *C.sqlite3_stmt, col int) string {
	ptr := C.c_sqlite_column_text(stmt, C.int(col))
	if ptr == nil {
		return ""
	}
	return C.GoString(ptr)
}

func C_colInt64(stmt *C.sqlite3_stmt, col int) int64 {
	return int64(C.c_sqlite_column_int64(stmt, C.int(col)))
}

func C_colDouble(stmt *C.sqlite3_stmt, col int) float64 {
	return float64(C.c_sqlite_column_double(stmt, C.int(col)))
}
