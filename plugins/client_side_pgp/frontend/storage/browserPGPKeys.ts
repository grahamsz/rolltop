import type { IdentityPGPPrivateKey } from "../../../../frontend/src/types";

const dbName = "rolltop-cache";
const legacyDbName = "rolltop-pgp-private-keys";
const dbVersion = 1;
const storeName = "entries";
const legacyStoreName = "privateKeys";

type StoredBrowserPGPKey = {
  id: string;
  user_id: number;
  key_id: number;
  identity_id: number;
  fingerprint: string;
  private_key_armored: string;
  updated_at: string;
};

function browserPGPKeyID(userID: number, keyID: number) {
  return `${userID}:${keyID}`;
}

function openBrowserPGPDB(name = dbName, objectStoreName = storeName): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (!("indexedDB" in window)) {
      reject(new Error("This browser does not support IndexedDB storage."));
      return;
    }
    const request = window.indexedDB.open(name, dbVersion);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(objectStoreName)) {
        db.createObjectStore(objectStoreName, { keyPath: "id" });
      }
    };
    request.onerror = () => reject(request.error || new Error("Could not open browser key storage."));
    request.onsuccess = () => resolve(request.result);
  });
}

async function withStore<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore) => IDBRequest<T> | void): Promise<T | undefined> {
  const db = await openBrowserPGPDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(storeName, mode);
    const store = tx.objectStore(storeName);
    const request = run(store);
    let result: T | undefined;
    if (request) {
      request.onsuccess = () => {
        result = request.result;
      };
      request.onerror = () => reject(request.error || new Error("Browser key storage failed."));
    }
    tx.oncomplete = () => {
      db.close();
      resolve(result);
    };
    tx.onerror = () => {
      db.close();
      reject(tx.error || new Error("Browser key storage failed."));
    };
  });
}

async function openStore<T>(name: string, objectStoreName: string, mode: IDBTransactionMode, run: (store: IDBObjectStore) => IDBRequest<T> | void): Promise<T | undefined> {
  try {
    const db = await openBrowserPGPDB(name, objectStoreName);
    return await new Promise((resolve, reject) => {
      const tx = db.transaction(objectStoreName, mode);
      const store = tx.objectStore(objectStoreName);
      const request = run(store);
      let result: T | undefined;
      if (request) {
        request.onsuccess = () => {
          result = request.result;
        };
        request.onerror = () => reject(request.error || new Error("Browser key storage failed."));
      }
      tx.oncomplete = () => {
        db.close();
        resolve(result);
      };
      tx.onerror = () => {
        db.close();
        reject(tx.error || new Error("Browser key storage failed."));
      };
    });
  } catch {
    return undefined;
  }
}

export async function saveBrowserPGPPrivateKey(userID: number, key: IdentityPGPPrivateKey, privateKeyArmored: string) {
  const keyID = key.id || 0;
  if (!userID || !keyID || !privateKeyArmored.trim()) {
    throw new Error("Could not save this private key in browser storage.");
  }
  const record: StoredBrowserPGPKey = {
    id: browserPGPKeyID(userID, keyID),
    user_id: userID,
    key_id: keyID,
    identity_id: key.identity_id,
    fingerprint: key.fingerprint,
    private_key_armored: privateKeyArmored.trim(),
    updated_at: new Date().toISOString()
  };
  await withStore("readwrite", (store) => store.put(record));
}

export async function loadBrowserPGPPrivateKey(userID: number, keyID: number): Promise<string> {
  const id = browserPGPKeyID(userID, keyID);
  const record = await withStore<StoredBrowserPGPKey>("readonly", (store) => store.get(id));
  if (record?.private_key_armored) return record.private_key_armored;
  const legacy = await openStore<StoredBrowserPGPKey>(legacyDbName, legacyStoreName, "readonly", (store) => store.get(id));
  if (legacy?.private_key_armored) {
    await withStore("readwrite", (store) => store.put(legacy));
    await openStore(legacyDbName, legacyStoreName, "readwrite", (store) => store.delete(id));
    return legacy.private_key_armored;
  }
  return "";
}

export async function deleteBrowserPGPPrivateKey(userID: number, keyID: number) {
  if (!userID || !keyID) return;
  await withStore("readwrite", (store) => store.delete(browserPGPKeyID(userID, keyID)));
  await openStore(legacyDbName, legacyStoreName, "readwrite", (store) => store.delete(browserPGPKeyID(userID, keyID)));
}

export async function hydrateBrowserPGPPrivateKeys(userID: number, keys: IdentityPGPPrivateKey[]): Promise<IdentityPGPPrivateKey[]> {
  const out: IdentityPGPPrivateKey[] = [];
  for (const key of keys) {
    if (key.private_key_storage === "browser" && key.id && !key.private_key_armored) {
      out.push({ ...key, private_key_armored: await loadBrowserPGPPrivateKey(userID, key.id) });
    } else {
      out.push(key);
    }
  }
  return out;
}
