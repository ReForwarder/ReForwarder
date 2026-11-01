import api from './client'
import type { ApiResponse, PaginatedResponse, ContactList, ContactListWithCount, ContactListMember } from './types'

export const contactListsApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<ContactListWithCount>>('/users/me/contact-lists', { params: { page, size } })
  },
  create(name: string, description: string) {
    return api.post<ApiResponse<ContactList>>('/users/me/contact-lists', { name, description })
  },
  update(id: number, name: string, description: string) {
    return api.put<ApiResponse<ContactList>>(`/users/me/contact-lists/${id}`, { name, description })
  },
  delete(id: number) {
    return api.delete(`/users/me/contact-lists/${id}`)
  },
  listMembers(id: number, page = 0, size = 20) {
    return api.get<PaginatedResponse<ContactListMember>>(`/users/me/contact-lists/${id}/members`, { params: { page, size } })
  },
  addMember(id: number, email: string, name: string) {
    return api.post<ApiResponse<ContactListMember>>(`/users/me/contact-lists/${id}/members`, { email, name })
  },
  removeMember(id: number, email: string) {
    return api.delete(`/users/me/contact-lists/${id}/members`, { data: { email } })
  },
}
