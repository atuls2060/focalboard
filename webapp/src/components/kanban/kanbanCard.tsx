// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.
import React, {useRef} from 'react'
import {injectIntl, IntlShape} from 'react-intl'
import {useDrag, useDrop} from 'react-dnd'

import {IPropertyTemplate} from '../../blocks/board'
import {Card} from '../../blocks/card'
import mutator from '../../mutator'
import IconButton from '../../widgets/buttons/iconButton'
import DeleteIcon from '../../widgets/icons/delete'
import DuplicateIcon from '../../widgets/icons/duplicate'
import OptionsIcon from '../../widgets/icons/options'
import Menu from '../../widgets/menu'
import MenuWrapper from '../../widgets/menuWrapper'

import './kanbanCard.scss'
import PropertyValueElement from '../propertyValueElement'

type Props = {
    card: Card
    visiblePropertyTemplates: IPropertyTemplate[]
    isSelected: boolean
    onClick?: (e: React.MouseEvent<HTMLDivElement>) => void
    intl: IntlShape
    readonly: boolean
    onDrop: (srcCard: Card, dstCard: Card) => void
}

const KanbanCard = React.memo((props: Props) => {
    const cardRef = useRef<HTMLDivElement>(null)
    const {card, intl, isSelected} = props
    const [{isDragging}, drag] = useDrag(() => ({
        type: 'card',
        item: card,
        collect: (monitor) => ({
            isDragging: monitor.isDragging(),
        }),
    }), [card])
    const [{isOver}, drop] = useDrop(() => ({
        accept: 'card',
        collect: (monitor) => ({
            isOver: monitor.isOver(),
        }),
        drop: (item: Card) => {
            props.onDrop(item, card)
        },
    }), [card, props.onDrop])

    const visiblePropertyTemplates = props.visiblePropertyTemplates || []
    let className = props.isSelected ? 'KanbanCard selected' : 'KanbanCard'
    if (isOver) {
        className += ' dragover'
    }

    drop(drag(cardRef))

    return (
        <div
            ref={props.readonly ? () => null : cardRef}
            className={className}
            draggable={!props.readonly}
            style={{opacity: isDragging ? 0.5 : 1}}
            onClick={props.onClick}
        >
            {!props.readonly &&
                <MenuWrapper
                    className='optionsMenu'
                    stopPropagationOnToggle={true}
                >
                    <IconButton icon={<OptionsIcon/>}/>
                    <Menu position='left'>
                        <Menu.Text
                            icon={<DeleteIcon/>}
                            id='delete'
                            name={intl.formatMessage({id: 'KanbanCard.delete', defaultMessage: 'Delete'})}
                            onClick={() => mutator.deleteBlock(card, 'delete card')}
                        />
                        <Menu.Text
                            icon={<DuplicateIcon/>}
                            id='duplicate'
                            name={intl.formatMessage({id: 'KanbanCard.duplicate', defaultMessage: 'Duplicate'})}
                            onClick={() => {
                                mutator.duplicateCard(card.id)
                            }}
                        />
                    </Menu>
                </MenuWrapper>
            }

            <div className='octo-icontitle'>
                { card.icon ? <div className='octo-icon'>{card.icon}</div> : undefined }
                <div key='__title'>{card.title || intl.formatMessage({id: 'KanbanCard.untitled', defaultMessage: 'Untitled'})}</div>
            </div>
            {visiblePropertyTemplates.map((template) => (
                <PropertyValueElement
                    key={template.id}
                    readOnly={true}
                    card={card}
                    propertyTemplate={template}
                    emptyDisplayValue=''
                />
            ))}
        </div>
    )
})

export default injectIntl(KanbanCard)
