export const MAX_DOCUMENT_BYTES = 100 * 1024 * 1024;

export const DOCUMENT_ACCEPT = [
  ".xlsx",
  ".xls",
  ".docx",
  ".doc",
  ".pdf",
].join(",");

const DOCUMENT_EXTENSION = /\.(xlsx|xls|docx|doc|pdf)$/i;

type DocumentCandidate = Pick<File, "name" | "size">;

export function validateProjectDocument(file: DocumentCandidate): string | null {
  if (!DOCUMENT_EXTENSION.test(file.name)) {
    return `${file.name} er ikke et understøttet dokument. Vælg Excel, Word eller PDF.`;
  }
  if (file.size > MAX_DOCUMENT_BYTES) {
    return `${file.name} er over 100 MB.`;
  }
  return null;
}

export function addProjectDocuments(
  current: File[],
  candidates: Iterable<File>,
): { documents: File[]; error: string } {
  const documents = [...current];
  let error = "";

  for (const file of candidates) {
    const validationError = validateProjectDocument(file);
    if (validationError) {
      error ||= validationError;
      continue;
    }
    documents.push(file);
  }

  return { documents, error };
}

export async function uploadProjectFiles<T>(
  files: readonly File[],
  upload: (file: File) => Promise<T>,
): Promise<T[]> {
  const uploaded: T[] = [];
  for (const file of files) {
    try {
      uploaded.push(await upload(file));
    } catch (error) {
      const detail = error instanceof Error && error.message ? `: ${error.message}` : "";
      throw new Error(`Kunne ikke uploade ${file.name}${detail}`, { cause: error });
    }
  }
  return uploaded;
}
