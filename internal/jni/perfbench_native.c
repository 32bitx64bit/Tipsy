/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include "perfbench_native.h"

#if defined(TIPSY_PERFBENCH)

#include <pthread.h>
#include <string.h>
#include <time.h>

static uint64_t elapsed_ns(const struct timespec *start, const struct timespec *end)
{
	return (uint64_t)(end->tv_sec - start->tv_sec) * UINT64_C(1000000000) +
		(uint64_t)(end->tv_nsec - start->tv_nsec);
}

int tipsy_jni_perf_make_object(JNIEnv *env, uintptr_t *out)
{
	jclass clazz;
	jobject local;
	jobject global;

	if (env == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*out = 0;
	clazz = env->functions->FindClass(env, "java/lang/Object");
	if (clazz == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	local = env->functions->AllocObject(env, clazz);
	if (local == NULL) {
		env->functions->DeleteLocalRef(env, clazz);
		return TIPSY_JNI_PERF_SETUP;
	}
	global = env->functions->NewGlobalRef(env, local);
	env->functions->DeleteLocalRef(env, local);
	env->functions->DeleteLocalRef(env, clazz);
	if (global == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*out = (uintptr_t)global;
	return TIPSY_JNI_PERF_OK;
}

int tipsy_jni_perf_make_class(JNIEnv *env, const char *name, uintptr_t *out)
{
	jclass local;
	jobject global;

	if (env == NULL || name == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*out = 0;
	local = env->functions->FindClass(env, name);
	if (local == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	global = env->functions->NewGlobalRef(env, local);
	env->functions->DeleteLocalRef(env, local);
	if (global == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*out = (uintptr_t)global;
	return TIPSY_JNI_PERF_OK;
}

int tipsy_jni_perf_make_field_object(JNIEnv *env, const char *class_name, uintptr_t *out)
{
	jclass clazz;
	jobject local;
	jobject global;

	if (env == NULL || class_name == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*out = 0;
	clazz = env->functions->FindClass(env, class_name);
	if (clazz == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	local = env->functions->AllocObject(env, clazz);
	if (local == NULL) {
		env->functions->DeleteLocalRef(env, clazz);
		return TIPSY_JNI_PERF_SETUP;
	}
	global = env->functions->NewGlobalRef(env, local);
	env->functions->DeleteLocalRef(env, local);
	env->functions->DeleteLocalRef(env, clazz);
	if (global == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*out = (uintptr_t)global;
	return TIPSY_JNI_PERF_OK;
}

int tipsy_jni_perf_make_dispatch(JNIEnv *env, uintptr_t *clazz_out, uintptr_t *method_out)
{
	jclass clazz;
	jobject global;
	jmethodID method;

	if (env == NULL || clazz_out == NULL || method_out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*clazz_out = 0;
	*method_out = 0;
	clazz = env->functions->FindClass(env, "com/roblox/client/LocalStorageManager");
	if (clazz == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	method = env->functions->GetStaticMethodID(env, clazz, "getAllocatableBytes", "()J");
	global = env->functions->NewGlobalRef(env, clazz);
	env->functions->DeleteLocalRef(env, clazz);
	if (method == NULL || global == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*clazz_out = (uintptr_t)global;
	*method_out = (uintptr_t)method;
	return TIPSY_JNI_PERF_OK;
}

int tipsy_jni_perf_make_field_method(JNIEnv *env, const char *class_name, uintptr_t *out)
{
	jclass clazz;
	jmethodID method;

	if (env == NULL || class_name == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*out = 0;
	clazz = env->functions->FindClass(env, class_name);
	if (clazz == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	method = env->functions->GetMethodID(env, clazz, "getUsername", "()Ljava/lang/String;");
	env->functions->DeleteLocalRef(env, clazz);
	if (method == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*out = (uintptr_t)method;
	return TIPSY_JNI_PERF_OK;
}

int tipsy_jni_perf_make_string(JNIEnv *env, const char *value, uintptr_t *out)
{
	jstring local;
	jobject global;

	if (env == NULL || value == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	*out = 0;
	local = env->functions->NewStringUTF(env, value);
	if (local == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	global = env->functions->NewGlobalRef(env, local);
	env->functions->DeleteLocalRef(env, local);
	if (global == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	*out = (uintptr_t)global;
	return TIPSY_JNI_PERF_OK;
}

void tipsy_jni_perf_delete_global(JNIEnv *env, uintptr_t ref)
{
	if (env != NULL && ref != 0) {
		env->functions->DeleteGlobalRef(env, (jobject)ref);
	}
}

struct perf_worker {
	JavaVM *vm;
	const struct tipsy_jni_perf_config *cfg;
	struct tipsy_jni_perf_result *result;
};

static int prepare_exception(JNIEnv *env, int pending)
{
	jclass throwable;
	jint rc;

	env->functions->ExceptionClear(env);
	if (!pending) {
		return TIPSY_JNI_PERF_OK;
	}
	throwable = env->functions->FindClass(env, "java/lang/Throwable");
	if (throwable == NULL) {
		return TIPSY_JNI_PERF_SETUP;
	}
	rc = env->functions->ThrowNew(env, throwable, "tipsy perfbench pending exception");
	env->functions->DeleteLocalRef(env, throwable);
	if (rc != JNI_OK || !env->functions->ExceptionCheck(env)) {
		return TIPSY_JNI_PERF_SETUP;
	}
	return TIPSY_JNI_PERF_OK;
}

static int consume_string_chars(JNIEnv *env, jstring value, const struct tipsy_jni_perf_config *cfg,
	int query_length, uint64_t *checksum)
{
	jboolean is_copy = JNI_FALSE;
	const jchar *chars;
	jsize length;
	uint64_t sum = 0;
	jsize i;

	chars = env->functions->GetStringChars(env, value, &is_copy);
	if (chars == NULL || is_copy != JNI_TRUE) {
		return TIPSY_JNI_PERF_EXPECTED;
	}
	length = query_length ? env->functions->GetStringLength(env, value) : (jsize)cfg->expected;
	if ((int64_t)length != cfg->expected) {
		env->functions->ReleaseStringChars(env, value, chars);
		return TIPSY_JNI_PERF_EXPECTED;
	}
	for (i = 0; i < length; i++) {
		sum += chars[i];
	}
	if (chars[length] != 0 || sum != cfg->expected_checksum) {
		env->functions->ReleaseStringChars(env, value, chars);
		return TIPSY_JNI_PERF_EXPECTED;
	}
	env->functions->ReleaseStringChars(env, value, chars);
	*checksum = sum;
	return TIPSY_JNI_PERF_OK;
}

static int field_getter_once(JNIEnv *env, const struct tipsy_jni_perf_config *cfg, uint64_t *checksum)
{
	jobject value;
	int status;

	value = env->functions->CallObjectMethodA(env, (jobject)cfg->object_a,
		(jmethodID)cfg->dispatch_method, NULL);
	if (value == NULL) {
		return TIPSY_JNI_PERF_EXPECTED;
	}
	status = consume_string_chars(env, (jstring)value, cfg, 1, checksum);
	env->functions->DeleteLocalRef(env, value);
	return status;
}

static int string_chars_once(JNIEnv *env, const struct tipsy_jni_perf_config *cfg, int query_length,
	uint64_t *checksum)
{
	return consume_string_chars(env, (jstring)cfg->object_a, cfg, query_length, checksum);
}

static int string_new_delete_once(JNIEnv *env, const struct tipsy_jni_perf_config *cfg,
	int validate_length, uint64_t *checksum)
{
	jstring value;
	jsize length = 0;

	if (cfg->string_utf == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	value = env->functions->NewStringUTF(env, cfg->string_utf);
	if (value == NULL) {
		return TIPSY_JNI_PERF_EXPECTED;
	}
	if (validate_length) {
		length = env->functions->GetStringLength(env, value);
		if ((int64_t)length != cfg->expected) {
			env->functions->DeleteLocalRef(env, value);
			return TIPSY_JNI_PERF_EXPECTED;
		}
	}
	env->functions->DeleteLocalRef(env, value);
	*checksum = (uint64_t)(validate_length ? length : cfg->expected);
	return TIPSY_JNI_PERF_OK;
}

static int run_loop(JNIEnv *env, const struct tipsy_jni_perf_config *cfg,
	struct tipsy_jni_perf_result *result)
{
	struct timespec start;
	struct timespec end;
	volatile uint64_t checksum = 0;
	uintptr_t last = 0;
	uint64_t i;
	int pending;

	if (cfg->iterations == 0) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	pending = cfg->kind == TIPSY_JNI_PERF_EXCEPTION_PENDING;
	if (cfg->kind == TIPSY_JNI_PERF_EXCEPTION_CLEAR || pending) {
		int rc = prepare_exception(env, pending);
		if (rc != TIPSY_JNI_PERF_OK) {
			return rc;
		}
	}

	/* Warm one real vtable call before the measured loop. */
	switch (cfg->kind) {
	case TIPSY_JNI_PERF_EXCEPTION_CLEAR:
	case TIPSY_JNI_PERF_EXCEPTION_PENDING:
		(void)env->functions->ExceptionCheck(env);
		break;
	case TIPSY_JNI_PERF_LOCAL_REF_PAIR: {
		jobject local = env->functions->NewLocalRef(env, (jobject)cfg->object_a);
		if (local == NULL) {
			return TIPSY_JNI_PERF_SETUP;
		}
		env->functions->DeleteLocalRef(env, local);
		break;
	}
	case TIPSY_JNI_PERF_DISPATCH_CORE_HIT:
		(void)env->functions->CallStaticLongMethodA(env, (jclass)cfg->dispatch_class,
			(jmethodID)cfg->dispatch_method, NULL);
		break;
	case TIPSY_JNI_PERF_IS_SAME_OBJECT:
		(void)env->functions->IsSameObject(env, (jobject)cfg->object_a, (jobject)cfg->object_b);
		break;
	case TIPSY_JNI_PERF_IS_INSTANCE_OF:
		(void)env->functions->IsInstanceOf(env, (jobject)cfg->object_a,
			(jclass)cfg->dispatch_class);
		break;
	case TIPSY_JNI_PERF_GET_VERSION:
		(void)env->functions->GetVersion(env);
		break;
	case TIPSY_JNI_PERF_FIELD_GETTER_STRING: {
		uint64_t warm_checksum;
		if (field_getter_once(env, cfg, &warm_checksum) != TIPSY_JNI_PERF_OK) {
			return TIPSY_JNI_PERF_SETUP;
		}
		break;
	}
	case TIPSY_JNI_PERF_STRING_CHARS: {
		uint64_t warm_checksum;
		if (string_chars_once(env, cfg, 1, &warm_checksum) != TIPSY_JNI_PERF_OK) {
			return TIPSY_JNI_PERF_SETUP;
		}
		break;
	}
	case TIPSY_JNI_PERF_STRING_CHARS_KNOWN_LENGTH: {
		uint64_t warm_checksum;
		if (string_chars_once(env, cfg, 0, &warm_checksum) != TIPSY_JNI_PERF_OK) {
			return TIPSY_JNI_PERF_SETUP;
		}
		break;
	}
	case TIPSY_JNI_PERF_NEW_STRING_UTF_DELETE: {
		uint64_t warm_checksum;
		if (string_new_delete_once(env, cfg, 1, &warm_checksum) != TIPSY_JNI_PERF_OK) {
			return TIPSY_JNI_PERF_SETUP;
		}
		break;
	}
	default:
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}

	if (clock_gettime(CLOCK_MONOTONIC, &start) != 0) {
		return TIPSY_JNI_PERF_CLOCK;
	}
	for (i = 0; i < cfg->iterations; i++) {
		switch (cfg->kind) {
		case TIPSY_JNI_PERF_EXCEPTION_CLEAR:
		case TIPSY_JNI_PERF_EXCEPTION_PENDING: {
			jboolean value = env->functions->ExceptionCheck(env);
			if ((value != JNI_FALSE) != pending) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uint64_t)value;
			last = (uintptr_t)value;
			break;
		}
		case TIPSY_JNI_PERF_LOCAL_REF_PAIR: {
			jobject local = env->functions->NewLocalRef(env, (jobject)cfg->object_a);
			if (local == NULL) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uintptr_t)local;
			last = (uintptr_t)local;
			env->functions->DeleteLocalRef(env, local);
			break;
		}
		case TIPSY_JNI_PERF_DISPATCH_CORE_HIT: {
			jlong value = env->functions->CallStaticLongMethodA(env, (jclass)cfg->dispatch_class,
				(jmethodID)cfg->dispatch_method, NULL);
			if (value != (jlong)cfg->expected) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uint64_t)value;
			last = (uintptr_t)value;
			break;
		}
		case TIPSY_JNI_PERF_IS_SAME_OBJECT: {
			jboolean value = env->functions->IsSameObject(env, (jobject)cfg->object_a, (jobject)cfg->object_b);
			if (value != (jboolean)cfg->expected) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uint64_t)value;
			last = (uintptr_t)value;
			break;
		}
		case TIPSY_JNI_PERF_IS_INSTANCE_OF: {
			jboolean value = env->functions->IsInstanceOf(env, (jobject)cfg->object_a,
				(jclass)cfg->dispatch_class);
			if (value != (jboolean)cfg->expected) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uint64_t)value;
			last = (uintptr_t)value;
			break;
		}
		case TIPSY_JNI_PERF_GET_VERSION: {
			jint value = env->functions->GetVersion(env);
			if (value != (jint)cfg->expected) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += (uint64_t)(uint32_t)value;
			last = (uintptr_t)(uint32_t)value;
			break;
		}
		case TIPSY_JNI_PERF_FIELD_GETTER_STRING: {
			uint64_t value_checksum;
			if (field_getter_once(env, cfg, &value_checksum) != TIPSY_JNI_PERF_OK) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += value_checksum;
			last = (uintptr_t)cfg->expected;
			break;
		}
		case TIPSY_JNI_PERF_STRING_CHARS: {
			uint64_t value_checksum;
			if (string_chars_once(env, cfg, 1, &value_checksum) != TIPSY_JNI_PERF_OK) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += value_checksum;
			last = (uintptr_t)cfg->expected;
			break;
		}
		case TIPSY_JNI_PERF_STRING_CHARS_KNOWN_LENGTH: {
			uint64_t value_checksum;
			if (string_chars_once(env, cfg, 0, &value_checksum) != TIPSY_JNI_PERF_OK) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += value_checksum;
			last = (uintptr_t)cfg->expected;
			break;
		}
		case TIPSY_JNI_PERF_NEW_STRING_UTF_DELETE: {
			uint64_t value_checksum;
			if (string_new_delete_once(env, cfg, 0, &value_checksum) != TIPSY_JNI_PERF_OK) {
				return TIPSY_JNI_PERF_EXPECTED;
			}
			checksum += value_checksum;
			last = (uintptr_t)cfg->expected;
			break;
		}
		default:
			return TIPSY_JNI_PERF_BAD_CONFIG;
		}
	}
	if (clock_gettime(CLOCK_MONOTONIC, &end) != 0) {
		return TIPSY_JNI_PERF_CLOCK;
	}
	result->elapsed_ns = elapsed_ns(&start, &end);
	result->operations = cfg->iterations;
	result->checksum = checksum;
	result->last_value = last;
	if (pending) {
		env->functions->ExceptionClear(env);
	}
	return TIPSY_JNI_PERF_OK;
}

static void *perf_worker_main(void *opaque)
{
	struct perf_worker *worker = opaque;
	JNIEnv *env = NULL;
	int status;

	worker->result->attach_rc = worker->vm->functions->AttachCurrentThread(worker->vm, &env, NULL);
	if (worker->result->attach_rc != JNI_OK || env == NULL) {
		worker->result->status = TIPSY_JNI_PERF_ATTACH;
		return NULL;
	}
	status = run_loop(env, worker->cfg, worker->result);
	worker->result->detach_rc = worker->vm->functions->DetachCurrentThread(worker->vm);
	if (status == TIPSY_JNI_PERF_OK && worker->result->detach_rc != JNI_OK) {
		status = TIPSY_JNI_PERF_DETACH;
	}
	worker->result->status = status;
	return NULL;
}

int tipsy_jni_perf_run(JavaVM *vm, const struct tipsy_jni_perf_config *cfg,
	struct tipsy_jni_perf_result *out)
{
	struct perf_worker worker;
	pthread_t thread;

	if (vm == NULL || cfg == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	memset(out, 0, sizeof(*out));
	worker.vm = vm;
	worker.cfg = cfg;
	worker.result = out;
	if (pthread_create(&thread, NULL, perf_worker_main, &worker) != 0) {
		out->status = TIPSY_JNI_PERF_THREAD_CREATE;
		return out->status;
	}
	(void)pthread_join(thread, NULL);
	return out->status;
}

struct isolation_worker {
	JavaVM *vm;
	pthread_mutex_t mu;
	pthread_cond_t cv;
	int ready;
	int release;
	int clear_attach_rc;
	int pending_attach_rc;
	int clear_detach_rc;
	int pending_detach_rc;
	int clear_value;
	int pending_value;
	int finished;
};

struct isolation_thread_arg {
	struct isolation_worker *shared;
	int pending;
};

static void *isolation_thread_main(void *opaque)
{
	struct isolation_thread_arg *arg = opaque;
	struct isolation_worker *shared = arg->shared;
	JNIEnv *env = NULL;
	int attach_rc;
	int value = -1;

	attach_rc = shared->vm->functions->AttachCurrentThread(shared->vm, &env, NULL);
	if (attach_rc == JNI_OK && env != NULL) {
		if (prepare_exception(env, arg->pending) != TIPSY_JNI_PERF_OK) {
			attach_rc = TIPSY_JNI_PERF_SETUP;
		}
	}
	pthread_mutex_lock(&shared->mu);
	if (arg->pending) {
		shared->pending_attach_rc = attach_rc;
	} else {
		shared->clear_attach_rc = attach_rc;
	}
	shared->ready++;
	pthread_cond_broadcast(&shared->cv);
	while (!shared->release) {
		pthread_cond_wait(&shared->cv, &shared->mu);
	}
	pthread_mutex_unlock(&shared->mu);
	if (attach_rc == JNI_OK && env != NULL) {
		value = env->functions->ExceptionCheck(env) != JNI_FALSE;
		env->functions->ExceptionClear(env);
		attach_rc = shared->vm->functions->DetachCurrentThread(shared->vm);
	}
	pthread_mutex_lock(&shared->mu);
	if (arg->pending) {
		shared->pending_value = value;
		shared->pending_detach_rc = attach_rc;
	} else {
		shared->clear_value = value;
		shared->clear_detach_rc = attach_rc;
	}
	shared->finished++;
	pthread_cond_broadcast(&shared->cv);
	pthread_mutex_unlock(&shared->mu);
	return NULL;
}

int tipsy_jni_perf_check_thread_exception_isolation(JavaVM *vm,
	struct tipsy_jni_perf_result *out)
{
	struct isolation_worker shared;
	struct isolation_thread_arg clear_arg;
	struct isolation_thread_arg pending_arg;
	pthread_t clear_thread;
	pthread_t pending_thread;

	if (vm == NULL || out == NULL) {
		return TIPSY_JNI_PERF_BAD_CONFIG;
	}
	memset(out, 0, sizeof(*out));
	memset(&shared, 0, sizeof(shared));
	shared.vm = vm;
	if (pthread_mutex_init(&shared.mu, NULL) != 0 || pthread_cond_init(&shared.cv, NULL) != 0) {
		out->status = TIPSY_JNI_PERF_THREAD_CREATE;
		return out->status;
	}
	clear_arg.shared = &shared;
	clear_arg.pending = 0;
	pending_arg.shared = &shared;
	pending_arg.pending = 1;
	if (pthread_create(&clear_thread, NULL, isolation_thread_main, &clear_arg) != 0) {
		pthread_cond_destroy(&shared.cv);
		pthread_mutex_destroy(&shared.mu);
		out->status = TIPSY_JNI_PERF_THREAD_CREATE;
		return out->status;
	}
	if (pthread_create(&pending_thread, NULL, isolation_thread_main, &pending_arg) != 0) {
		/* This branch is only a harness setup failure; release and join the
		 * first worker before reclaiming its synchronization primitives. */
		pthread_mutex_lock(&shared.mu);
		shared.release = 1;
		pthread_cond_broadcast(&shared.cv);
		pthread_mutex_unlock(&shared.mu);
		(void)pthread_join(clear_thread, NULL);
		pthread_cond_destroy(&shared.cv);
		pthread_mutex_destroy(&shared.mu);
		out->status = TIPSY_JNI_PERF_THREAD_CREATE;
		return out->status;
	}
	pthread_mutex_lock(&shared.mu);
	while (shared.ready != 2) {
		pthread_cond_wait(&shared.cv, &shared.mu);
	}
	shared.release = 1;
	pthread_cond_broadcast(&shared.cv);
	while (shared.finished != 2) {
		pthread_cond_wait(&shared.cv, &shared.mu);
	}
	pthread_mutex_unlock(&shared.mu);
	(void)pthread_join(clear_thread, NULL);
	(void)pthread_join(pending_thread, NULL);
	pthread_cond_destroy(&shared.cv);
	pthread_mutex_destroy(&shared.mu);

	out->attach_rc = shared.clear_attach_rc == JNI_OK && shared.pending_attach_rc == JNI_OK ? JNI_OK : JNI_ERR;
	out->detach_rc = shared.clear_detach_rc == JNI_OK && shared.pending_detach_rc == JNI_OK ? JNI_OK : JNI_ERR;
	out->last_value = (uintptr_t)((shared.clear_value & 0xff) | ((shared.pending_value & 0xff) << 8));
	out->checksum = (uint64_t)((shared.clear_value == 0) + (shared.pending_value == 1));
	out->operations = 2;
	out->status = out->attach_rc == JNI_OK && out->detach_rc == JNI_OK && out->checksum == 2
		? TIPSY_JNI_PERF_OK : TIPSY_JNI_PERF_EXPECTED;
	return out->status;
}

#endif /* TIPSY_PERFBENCH */
