//go:build darwin && cgo

package keychain

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#cgo CFLAGS: -Wno-deprecated-declarations

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <limits.h>
#include <pwd.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define TMCP_ERR_WRONG_KEYCHAIN ((OSStatus)-98765)

typedef struct {
    OSStatus status;
    void *bytes;
    size_t length;
} tmcp_read_result;

static int tmcp_is_login_keychain_path(const char *path) {
	struct passwd password;
	struct passwd *result = NULL;
	char password_buffer[16384];
	if (getpwuid_r(getuid(), &password, password_buffer, sizeof(password_buffer), &result) != 0 || result == NULL || result->pw_dir == NULL) {
		return 0;
	}

	char expected[PATH_MAX];
	int length = snprintf(expected, sizeof(expected), "%s/Library/Keychains/login.keychain-db", result->pw_dir);
	if (length > 0 && (size_t)length < sizeof(expected) && strcmp(path, expected) == 0) {
		return 1;
	}
	length = snprintf(expected, sizeof(expected), "%s/Library/Keychains/login.keychain", result->pw_dir);
	return length > 0 && (size_t)length < sizeof(expected) && strcmp(path, expected) == 0;
}

static OSStatus tmcp_copy_unlocked_login_keychain(SecKeychainRef *result) {
    *result = NULL;
    OSStatus status = SecKeychainCopyDefault(result);
    if (status != errSecSuccess) {
        return status;
    }

    char path[PATH_MAX];
    UInt32 path_length = sizeof(path);
    status = SecKeychainGetPath(*result, &path_length, path);
    if (status != errSecSuccess) {
        CFRelease(*result);
        *result = NULL;
        return status;
    }
    if (path_length >= sizeof(path)) {
        CFRelease(*result);
        *result = NULL;
        return errSecBufferTooSmall;
    }
    path[path_length] = '\0';
    if (!tmcp_is_login_keychain_path(path)) {
        CFRelease(*result);
        *result = NULL;
        return TMCP_ERR_WRONG_KEYCHAIN;
    }

    SecKeychainStatus keychain_status = 0;
    status = SecKeychainGetStatus(*result, &keychain_status);
    if (status != errSecSuccess) {
        CFRelease(*result);
        *result = NULL;
        return status;
    }
    if ((keychain_status & kSecUnlockStateStatus) == 0) {
        CFRelease(*result);
        *result = NULL;
        return errSecInteractionNotAllowed;
    }
    return errSecSuccess;
}

static CFStringRef tmcp_string(const char *value) {
    return CFStringCreateWithCString(kCFAllocatorDefault, value, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef tmcp_base_query(
    const char *service,
    const char *account,
    SecKeychainRef keychain,
    int for_add
) {
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(
        kCFAllocatorDefault,
        0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );
    if (query == NULL) {
        return NULL;
    }

    CFStringRef service_value = tmcp_string(service);
    CFStringRef account_value = tmcp_string(account);
    if (service_value == NULL || account_value == NULL) {
        if (service_value != NULL) CFRelease(service_value);
        if (account_value != NULL) CFRelease(account_value);
        CFRelease(query);
        return NULL;
    }

    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, service_value);
    CFDictionarySetValue(query, kSecAttrAccount, account_value);
    CFDictionarySetValue(query, kSecAttrSynchronizable, kCFBooleanFalse);
    CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
    CFRelease(service_value);
    CFRelease(account_value);

    if (for_add) {
        CFDictionarySetValue(query, kSecUseKeychain, keychain);
    } else {
        const void *values[] = { keychain };
        CFArrayRef search_list = CFArrayCreate(
            kCFAllocatorDefault,
            values,
            1,
            &kCFTypeArrayCallBacks
        );
        if (search_list == NULL) {
            CFRelease(query);
            return NULL;
        }
        CFDictionarySetValue(query, kSecMatchSearchList, search_list);
        CFRelease(search_list);
    }
    return query;
}

static OSStatus tmcp_add(const char *service, const char *account, const void *bytes, size_t length) {
    SecKeychainRef keychain = NULL;
    OSStatus status = tmcp_copy_unlocked_login_keychain(&keychain);
    if (status != errSecSuccess) return status;

    CFMutableDictionaryRef item = tmcp_base_query(service, account, keychain, 1);
    if (item == NULL) {
        CFRelease(keychain);
        return errSecAllocate;
    }
    CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, (CFIndex)length);
    CFStringRef label = tmcp_string("Telegram MCP local secret");
    if (data == NULL || label == NULL) {
        if (data != NULL) CFRelease(data);
        if (label != NULL) CFRelease(label);
        CFRelease(item);
        CFRelease(keychain);
        return errSecAllocate;
    }
    CFDictionarySetValue(item, kSecValueData, data);
    CFDictionarySetValue(item, kSecAttrLabel, label);
    status = SecItemAdd(item, NULL);

    CFRelease(label);
    CFRelease(data);
    CFRelease(item);
    CFRelease(keychain);
    return status;
}

static OSStatus tmcp_update(const char *service, const char *account, const void *bytes, size_t length) {
    SecKeychainRef keychain = NULL;
    OSStatus status = tmcp_copy_unlocked_login_keychain(&keychain);
    if (status != errSecSuccess) return status;

    CFMutableDictionaryRef query = tmcp_base_query(service, account, keychain, 0);
    CFMutableDictionaryRef changes = CFDictionaryCreateMutable(
        kCFAllocatorDefault,
        0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );
    CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, (CFIndex)length);
    if (query == NULL || changes == NULL || data == NULL) {
        if (query != NULL) CFRelease(query);
        if (changes != NULL) CFRelease(changes);
        if (data != NULL) CFRelease(data);
        CFRelease(keychain);
        return errSecAllocate;
    }
    CFDictionarySetValue(changes, kSecValueData, data);
    status = SecItemUpdate(query, changes);

    CFRelease(data);
    CFRelease(changes);
    CFRelease(query);
    CFRelease(keychain);
    return status;
}

static OSStatus tmcp_put(const char *service, const char *account, const void *bytes, size_t length) {
    OSStatus status = tmcp_add(service, account, bytes, length);
    if (status == errSecDuplicateItem) {
        status = tmcp_update(service, account, bytes, length);
    }
    return status;
}

static tmcp_read_result tmcp_read(const char *service, const char *account, size_t maximum_length) {
    tmcp_read_result output = { errSecSuccess, NULL, 0 };
    SecKeychainRef keychain = NULL;
    output.status = tmcp_copy_unlocked_login_keychain(&keychain);
    if (output.status != errSecSuccess) return output;

    CFMutableDictionaryRef query = tmcp_base_query(service, account, keychain, 0);
    if (query == NULL) {
        output.status = errSecAllocate;
        CFRelease(keychain);
        return output;
    }
    CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);

    CFTypeRef result = NULL;
    output.status = SecItemCopyMatching(query, &result);
    if (output.status == errSecSuccess) {
        if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
            output.status = errSecDecode;
        } else {
            CFDataRef data = (CFDataRef)result;
            CFIndex length = CFDataGetLength(data);
            if (length <= 0 || (uint64_t)length > maximum_length) {
                output.status = errSecDecode;
            } else {
                output.bytes = malloc((size_t)length);
                if (output.bytes == NULL) {
                    output.status = errSecAllocate;
                } else {
                    CFDataGetBytes(data, CFRangeMake(0, length), output.bytes);
                    output.length = (size_t)length;
                }
            }
        }
    }

    if (result != NULL) CFRelease(result);
    CFRelease(query);
    CFRelease(keychain);
    return output;
}

static OSStatus tmcp_delete(const char *service, const char *account) {
    SecKeychainRef keychain = NULL;
    OSStatus status = tmcp_copy_unlocked_login_keychain(&keychain);
    if (status != errSecSuccess) return status;

    CFMutableDictionaryRef query = tmcp_base_query(service, account, keychain, 0);
    if (query == NULL) {
        CFRelease(keychain);
        return errSecAllocate;
    }
    status = SecItemDelete(query);
    CFRelease(query);
    CFRelease(keychain);
    return status;
}

static void tmcp_free_secret(void *bytes, size_t length) {
    if (bytes == NULL) return;
    volatile unsigned char *cursor = (volatile unsigned char *)bytes;
    while (length-- > 0) *cursor++ = 0;
    free(bytes);
}

static int32_t tmcp_status_not_found(void) { return (int32_t)errSecItemNotFound; }
static int32_t tmcp_status_locked(void) { return (int32_t)errSecInteractionNotAllowed; }
static int32_t tmcp_status_wrong_keychain(void) { return (int32_t)TMCP_ERR_WRONG_KEYCHAIN; }
*/
import "C"

import (
	"errors"
	"unsafe"
)

func (s *Store) put(account string, secret []byte) error {
	serviceValue := C.CString(s.service)
	accountValue := C.CString(account)
	defer C.free(unsafe.Pointer(serviceValue))
	defer C.free(unsafe.Pointer(accountValue))
	status := int32(C.tmcp_put(
		serviceValue,
		accountValue,
		unsafe.Pointer(unsafe.SliceData(secret)),
		C.size_t(len(secret)),
	))
	return keychainError("store", status)
}

func (s *Store) get(account string) ([]byte, error) {
	serviceValue := C.CString(s.service)
	accountValue := C.CString(account)
	defer C.free(unsafe.Pointer(serviceValue))
	defer C.free(unsafe.Pointer(accountValue))
	result := C.tmcp_read(serviceValue, accountValue, C.size_t(MaximumSecretBytes))
	if err := keychainError("read", int32(result.status)); err != nil {
		if result.bytes != nil {
			C.tmcp_free_secret(result.bytes, result.length)
		}
		return nil, err
	}
	defer C.tmcp_free_secret(result.bytes, result.length)
	if result.length == 0 || result.length > C.size_t(MaximumSecretBytes) {
		return nil, errors.New("stored secret length is outside the allowed range")
	}
	return C.GoBytes(result.bytes, C.int(result.length)), nil
}

func (s *Store) delete(account string) error {
	serviceValue := C.CString(s.service)
	accountValue := C.CString(account)
	defer C.free(unsafe.Pointer(serviceValue))
	defer C.free(unsafe.Pointer(accountValue))
	status := int32(C.tmcp_delete(serviceValue, accountValue))
	return keychainError("delete", status)
}

func keychainError(operation string, status int32) error {
	if status == 0 {
		return nil
	}
	kind := error(nil)
	switch status {
	case int32(C.tmcp_status_not_found()):
		kind = ErrNotFound
	case int32(C.tmcp_status_locked()):
		kind = ErrKeychainLocked
	case int32(C.tmcp_status_wrong_keychain()):
		kind = ErrWrongKeychain
	}
	return &StatusError{Operation: operation, Status: status, kind: kind}
}
