export type BackendPagination = {
  offset: number;
  limit: number;
  returned: number;
  total: number;
  has_more: boolean;
  next_offset?: number;
};

export type BackendFileObject = {
  key: string;
  name: string;
  size: number;
  last_modified: string;
  content_type: string;
  etag: string;
};

export type BackendFolderEntry = {
  prefix: string;
  name: string;
};

export type BackendListResponse = {
  prefix: string;
  folders: BackendFolderEntry[];
  files: BackendFileObject[];
  pagination: BackendPagination;
};

export type BackendSearchResponse = {
  query: string;
  prefix: string;
  results: BackendFileObject[];
  pagination: BackendPagination;
};

export type BackendPresignResponse = {
  url: string;
  key: string;
  expires_in?: string;
};
